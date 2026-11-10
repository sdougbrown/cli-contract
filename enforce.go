package clicontract

import (
	"encoding/json"
	"strings"

	"github.com/umpire-tools/umpire-go"
)

// Input-rule enforcement: Run and Parse evaluate a command's declared rules
// against the invocation, so a declaration's own contract holds at parse time
// and a handler never re-checks a relationship the declaration already states.

// inputEnforcement is one command's compiled input rules plus the flag surface
// they read.
type inputEnforcement struct {
	engine  *umpire.Umpire
	surface []Flag
	// invocation reports a rule with no field, which constrains the whole
	// invocation rather than one flag.
	invocation bool
}

// parseSurface lists the flags one command parses in the order invocationFrom
// reads them: the app's common flags, the command's own, then each ancestor's
// persistent flags. Violations are reported in this order, so the message is
// stable across runs.
func parseSurface(c *Command, ancestors []*Command, common []Flag) []Flag {
	out := make([]Flag, 0, len(common)+len(c.Flags))
	out = append(out, common...)
	if len(c.Subcommands) == 0 {
		out = append(out, c.Flags...)
	}
	for _, ancestor := range ancestors {
		out = append(out, ancestor.PersistentFlags...)
	}
	return out
}

// suppliedValues builds the evaluator's input map from the flags the caller
// actually typed. Parsed values cannot serve: defaults are registered on the
// flag set, so every omitted flag with a default still carries a value and
// would satisfy every rule that names it.
func (inv Invocation) suppliedValues(surface []Flag) map[string]any {
	values := make(map[string]any, len(surface))
	for _, f := range surface {
		if !inv.Supplied[f.Name] {
			continue
		}
		switch f.Kind {
		case Bool:
			values[f.Name] = inv.Bools[f.Name]
		case Repeat:
			values[f.Name] = inv.Repeated[f.Name]
		case Int:
			// Umpire compares against JSON numbers, so an integer property
			// carries a float64 rather than the parsed int.
			values[f.Name] = float64(inv.Ints[f.Name])
		case Duration:
			// Duration rules compare the canonical Go duration string. The
			// parser normalizes the caller's spelling (0m evaluates as 0s),
			// and the spec makes a consumer normalize its input the same
			// way before evaluating the emitted rules.
			values[f.Name] = inv.Durations[f.Name].String()
		case DurationMS:
			// Rules compare whole-number milliseconds: the value the
			// caller supplied is already the value a consumer evaluates,
			// so no normalization statement exists for this kind.
			values[f.Name] = float64(inv.Durations[f.Name].Milliseconds())
		default:
			values[f.Name] = inv.Flags[f.Name]
		}
	}
	return values
}

// enforcement compiles one command's input rules against the surface it
// parses. One execution dispatches at most one command, so there is nothing to
// cache across calls.
func (a *App) enforcement(c *Command, ancestors []*Command) (*inputEnforcement, error) {
	enf := &inputEnforcement{surface: parseSurface(c, ancestors, a.CommonFlags)}
	if len(c.InputRules) == 0 {
		return enf, nil
	}
	engine, invocation, err := compileInputRules(enf.surface, c.InputRules)
	if err != nil {
		return nil, err
	}
	enf.engine = engine
	enf.invocation = invocation
	return enf, nil
}

// invocationField is the internal Umpire field an invocation-wide input rule
// is compiled onto. Umpire's requires and enabledWhen rules must name a field,
// while the declaration and the published documents carry a fieldless gate
// rule with no field. NUL keeps the internal name clear of every flag a caller
// could type, and it never reaches the wire.
const invocationField = "\x00invocation"

// compileInputRules builds the Umpire engine from the same wire shape the
// schema document and the manifest carry, so the runtime cannot evaluate a
// contract the generated documents do not describe. A fieldless gate rule is
// compiled onto invocationField; the returned flag reports whether any rule
// did, so enforcement still runs when the caller supplied no flags.
func compileInputRules(surface []Flag, rules []*Rule) (*umpire.Umpire, bool, error) {
	fields := make(map[string]any, len(surface)+1)
	for _, f := range surface {
		fields[f.Name] = map[string]any{}
	}
	compiled, invocation := compileRuleValues(rules)
	if invocation {
		fields[invocationField] = map[string]any{}
	}
	raw, err := json.Marshal(map[string]any{
		"version": float64(1),
		"fields":  fields,
		"rules":   compiled,
	})
	if err != nil {
		return nil, false, err
	}
	var block any
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, false, err
	}
	schema, err := umpire.SchemaFromObject(block)
	if err != nil {
		return nil, false, err
	}
	engine, err := umpire.NewFromSchema(schema)
	if err != nil {
		return nil, false, err
	}
	return engine, invocation, nil
}

// compileRuleValues copies the declared rules for the Umpire engine, giving a
// fieldless rule the internal invocation field. The declaration is not mutated:
// the schema document and the manifest render the rule without a field.
func compileRuleValues(rules []*Rule) ([]Rule, bool) {
	if len(rules) == 0 {
		return nil, false
	}
	out := make([]Rule, 0, len(rules))
	invocation := false
	for _, rule := range rules {
		if rule == nil {
			continue // rejected at startup; skip rather than panic here
		}
		value, usesInvocation := compileRuleValue(rule)
		invocation = invocation || usesInvocation
		out = append(out, value)
	}
	return out, invocation
}

// compileRuleValue copies one rule, recursing through composites so a fieldless
// gate operand reaches the engine with the invocation field too.
func compileRuleValue(rule *Rule) (Rule, bool) {
	out := *rule
	invocation := false
	if (out.Type == RuleEnabledWhen || out.Type == RuleRequires) && out.Field == "" {
		out.Field = invocationField
		invocation = true
	}
	if len(rule.Rules) > 0 {
		out.Rules = make([]*Rule, 0, len(rule.Rules))
		for _, inner := range rule.Rules {
			if inner == nil {
				out.Rules = append(out.Rules, nil)
				continue
			}
			value, usesInvocation := compileRuleValue(inner)
			invocation = invocation || usesInvocation
			out.Rules = append(out.Rules, &value)
		}
	}
	if len(rule.Branches) > 0 {
		out.Branches = make(map[string][]*Rule, len(rule.Branches))
		for name, branch := range rule.Branches {
			values := make([]*Rule, 0, len(branch))
			for _, inner := range branch {
				if inner == nil {
					values = append(values, nil)
					continue
				}
				value, usesInvocation := compileRuleValue(inner)
				invocation = invocation || usesInvocation
				values = append(values, &value)
			}
			out.Branches[name] = values
		}
	}
	return out, invocation
}

// enforceInputRules rejects an invocation that violates the command's declared
// input rules or omits a required flag, as a caller error: both are usage
// faults the caller can fix from the message.
func (a *App) enforceInputRules(exec *execution, c *Command, ancestors []*Command, inv Invocation) error {
	// The path every other dispatch error uses: inv.Path excludes the
	// top-level command, which Invocation.Command carries.
	path := strings.Join(append(commandNames(ancestors), c.Name), " ")

	enf, err := a.enforcement(c, ancestors)
	if err != nil {
		return err
	}
	for _, f := range enf.surface {
		if f.Required && !inv.Supplied[f.Name] {
			return a.errf(path, "missing required flag --%s", f.Name)
		}
	}
	if enf.engine == nil {
		return nil
	}
	values := inv.suppliedValues(enf.surface)
	// An invocation-wide rule has to run even when the caller supplied
	// nothing: that is the case it exists to reject. Field rules keep their
	// supplied-only meaning, so a run with no values and no invocation rule
	// cannot produce a violation.
	if len(values) == 0 && !enf.invocation {
		return nil
	}
	status, err := enf.engine.Check(values, nil, nil)
	if err != nil {
		return err
	}
	// The invocation-wide requirement is unconditional, so it is reported
	// before any supplied flag's rule.
	if enf.invocation {
		if availability, ok := status[invocationField]; ok && !availability.Enabled {
			reason := availability.Reason
			if reason == "" {
				reason = "input rules reject this invocation"
			}
			return a.errf(path, "%s", reason)
		}
	}
	// Report the first violation in declaration order so the message is
	// stable: the surface order is the one the help output uses.
	for _, f := range enf.surface {
		if !inv.Supplied[f.Name] {
			continue
		}
		availability, ok := status[f.Name]
		if !ok || availability.Enabled {
			continue
		}
		reason := availability.Reason
		if reason == "" {
			reason = "input rules reject this combination"
		}
		return a.errf(path, "%s", reason)
	}
	return nil
}
