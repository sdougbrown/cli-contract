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
			// Duration flags bind as their Go duration string in every
			// other surface, so rules compare against the same form.
			values[f.Name] = inv.Durations[f.Name].String()
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
	engine, err := compileInputRules(enf.surface, c.InputRules)
	if err != nil {
		return nil, err
	}
	enf.engine = engine
	return enf, nil
}

// compileInputRules builds the Umpire engine from the same wire shape the
// schema document and the manifest carry, so the runtime cannot evaluate a
// contract the generated documents do not describe.
func compileInputRules(surface []Flag, rules []*Rule) (*umpire.Umpire, error) {
	fields := make(map[string]any, len(surface))
	for _, f := range surface {
		fields[f.Name] = map[string]any{}
	}
	raw, err := json.Marshal(map[string]any{
		"version": float64(1),
		"fields":  fields,
		"rules":   ruleValues(rules),
	})
	if err != nil {
		return nil, err
	}
	var block any
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, err
	}
	schema, err := umpire.SchemaFromObject(block)
	if err != nil {
		return nil, err
	}
	return umpire.NewFromSchema(schema)
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
	if len(values) == 0 {
		return nil
	}
	status, err := enf.engine.Check(values, nil, nil)
	if err != nil {
		return err
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
