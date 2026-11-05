// Package registry renders a declared CLI's contract as a cli-registry
// manifest: the producer half of contract-as-data feeding the consumer half.
// It emits pure JSON and imports nothing from cli-registry; the manifest is
// the only coupling.
//
// See github.com/sdougbrown/cli-registry (specs/manifest.md, specs/argv.md)
// for the manifest contract this package targets.
package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sdougbrown/cli-contract"
)

// Options tunes the manifest generation.
type Options struct {
	// Binary is the argv prefix: the command that runs the CLI.
	Binary string
	// Timeouts maps a command path ("store reset") to its Go duration
	// string. Commands without an entry get DefaultTimeout.
	Timeouts map[string]string
	// DefaultTimeout is the timeout for commands without an entry. It must
	// be a valid Go duration string.
	DefaultTimeout string
	// CommonFlags names the declared common flags worth exposing to tool
	// calls. Flags not listed are omitted from the manifest.
	CommonFlags []string
}

// DefaultTimeout is used when Options.DefaultTimeout is empty.
const DefaultTimeout = "120s"

// Binding is one argv-binding row.
type Binding struct {
	Property string `json:"property"`
	Style    string `json:"style"`
	Flag     string `json:"flag,omitempty"`
}

// manifestShape mirrors the cli-registry manifest's normative shape.
type manifestShape struct {
	Version      int                  `json:"version"`
	Tools        map[string]toolShape `json:"tools"`
	Availability *availabilityShape   `json:"availability,omitempty"`
}

// availabilityShape is the registry-level availability block. Fields are
// implicit (the consumer derives them from the tool set); the block carries
// the declared conditions and the rules that gate tools. The rule shapes are
// local to this package — the wire contract must not move when the
// declaration structs change.
type availabilityShape struct {
	Conditions map[string]conditionShape `json:"conditions,omitempty"`
	Rules      []ruleShape               `json:"rules,omitempty"`
}

// ruleShape mirrors one Umpire availability rule on the wire.
type ruleShape struct {
	Type     string                 `json:"type"`
	Field    string                 `json:"field,omitempty"`
	Targets  []string               `json:"targets,omitempty"`
	Group    string                 `json:"group,omitempty"`
	When     *exprShape             `json:"when,omitempty"`
	Reason   string                 `json:"reason,omitempty"`
	Rules    []ruleShape            `json:"rules,omitempty"`
	Branches map[string][]ruleShape `json:"branches,omitempty"`
}

// exprShape mirrors one Umpire condition expression on the wire.
type exprShape struct {
	Op        string      `json:"op"`
	Field     string      `json:"field,omitempty"`
	Condition string      `json:"condition,omitempty"`
	Value     any         `json:"value,omitempty"`
	Values    []any       `json:"values,omitempty"`
	Exprs     []exprShape `json:"exprs,omitempty"`
	Expr      *exprShape  `json:"expr,omitempty"`
}

func ruleShapeFrom(rule *clicontract.Rule) ruleShape {
	out := ruleShape{
		Type:    rule.Type,
		Field:   rule.Field,
		Targets: rule.Targets,
		Group:   rule.Group,
		Reason:  rule.Reason,
	}
	if rule.When != nil {
		when := exprShapeFrom(rule.When)
		out.When = &when
	}
	if len(rule.Rules) > 0 {
		out.Rules = make([]ruleShape, 0, len(rule.Rules))
		for _, inner := range rule.Rules {
			out.Rules = append(out.Rules, ruleShapeFrom(inner))
		}
	}
	if len(rule.Branches) > 0 {
		out.Branches = make(map[string][]ruleShape, len(rule.Branches))
		for name, branch := range rule.Branches {
			shapes := make([]ruleShape, 0, len(branch))
			for _, inner := range branch {
				shapes = append(shapes, ruleShapeFrom(inner))
			}
			out.Branches[name] = shapes
		}
	}
	return out
}

func exprShapeFrom(expr *clicontract.Expr) exprShape {
	out := exprShape{
		Op:        expr.Op,
		Field:     expr.Field,
		Condition: expr.Condition,
		Value:     expr.Value,
		Values:    expr.Values,
	}
	if len(expr.Exprs) > 0 {
		out.Exprs = make([]exprShape, 0, len(expr.Exprs))
		for _, inner := range expr.Exprs {
			out.Exprs = append(out.Exprs, exprShapeFrom(inner))
		}
	}
	if expr.Expr != nil {
		inner := exprShapeFrom(expr.Expr)
		out.Expr = &inner
	}
	return out
}

// conditionShape is one declared availability input.
type conditionShape struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type toolShape struct {
	Description string         `json:"description"`
	Command     []string       `json:"command"`
	Timeout     string         `json:"timeout"`
	InputSchema map[string]any `json:"inputSchema"`
	InputRules  []ruleShape    `json:"inputRules,omitempty"`
	Bindings    []Binding      `json:"bindings"`
	Output      map[string]any `json:"output"`
}

// Manifest renders the declared commands as a cli-registry manifest,
// deterministically (sorted tools, properties, and bindings). Nested
// subcommands flatten to one tool per leaf ("store path" → "store-path").
// Availability rules declared on commands render into the top-level
// availability block; rules without an explicit field gate their own tool.
func Manifest(app *clicontract.App, opts Options) ([]byte, error) {
	timeout := opts.DefaultTimeout
	if timeout == "" {
		timeout = DefaultTimeout
	}
	tools := map[string]toolShape{}
	var rules []*clicontract.Rule
	var walk func(path []string, cmd *clicontract.Command) error
	walk = func(path []string, cmd *clicontract.Command) error {
		if len(cmd.Subcommands) > 0 {
			if len(cmd.Availability) > 0 || len(cmd.InputRules) > 0 {
				return fmt.Errorf(
					"command %q declares rules but owns subcommands; only leaf commands run as tools",
					strings.Join(path, " "),
				)
			}
			for _, sub := range cmd.Subcommands {
				if err := walk(append(path, sub.Name), sub); err != nil {
					return err
				}
			}
			return nil
		}
		name := clicontract.ToolName(app.Name, strings.Join(path, "-"))
		shape, err := buildTool(opts, timeout, app.Name, path, cmd, app.CommonFlags)
		if err != nil {
			return err
		}
		tools[name] = shape
		rules = append(rules, resolvedRules(name, cmd.Availability)...)
		return nil
	}
	for _, cmd := range app.Commands {
		if err := walk([]string{cmd.Name}, cmd); err != nil {
			return nil, err
		}
	}
	for name, t := range tools {
		bindings := t.Bindings
		sort.Slice(bindings, func(i, j int) bool {
			return bindings[i].Property < bindings[j].Property
		})
		t.Bindings = bindings
		tools[name] = t
	}
	rules, err := resolveTargets(app.Name, tools, rules)
	if err != nil {
		return nil, err
	}
	if err := validateResolvedConsistency(rules); err != nil {
		return nil, err
	}
	var availability *availabilityShape
	if len(app.AvailabilityConditions) > 0 || len(rules) > 0 {
		conditions := make(map[string]conditionShape, len(app.AvailabilityConditions))
		for _, c := range app.AvailabilityConditions {
			conditions[c.Name] = conditionShape{Type: c.Type, Description: c.Description}
		}
		shapes := make([]ruleShape, 0, len(rules))
		for _, rule := range rules {
			shapes = append(shapes, ruleShapeFrom(rule))
		}
		availability = &availabilityShape{Conditions: conditions, Rules: shapes}
	}
	b, err := json.MarshalIndent(manifestShape{Version: 1, Tools: tools, Availability: availability}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// resolvedRules assigns a rule's field: an explicit Field passes through; an
// empty Field takes the tool the rule was declared on (so anyOf operands and
// eitherOf branches gate the declaring command without repeating its name).
func resolvedRules(tool string, declared []*clicontract.Rule) []*clicontract.Rule {
	if len(declared) == 0 {
		return nil
	}
	var resolve func(rule *clicontract.Rule) *clicontract.Rule
	resolve = func(rule *clicontract.Rule) *clicontract.Rule {
		out := *rule
		// Only the field-targeting leaf forms carry a field; disables names
		// its tools through targets, and composite wrappers stay bare.
		if out.Field == "" && (out.Type == clicontract.RuleEnabledWhen || out.Type == clicontract.RuleRequires) {
			out.Field = tool
		}
		// Copy slice fields: a shallow struct copy would share Targets'
		// backing array with the caller's declaration, and resolveTargets
		// rewrites targets in place — the App must stay untouched.
		if len(rule.Targets) > 0 {
			out.Targets = append([]string(nil), rule.Targets...)
		}
		if len(rule.Rules) > 0 {
			out.Rules = nil
			for _, inner := range rule.Rules {
				out.Rules = append(out.Rules, resolve(inner))
			}
		}
		if len(rule.Branches) > 0 {
			out.Branches = make(map[string][]*clicontract.Rule, len(rule.Branches))
			for name, branch := range rule.Branches {
				resolvedBranch := make([]*clicontract.Rule, 0, len(branch))
				for _, inner := range branch {
					resolvedBranch = append(resolvedBranch, resolve(inner))
				}
				out.Branches[name] = resolvedBranch
			}
		}
		return &out
	}
	out := make([]*clicontract.Rule, 0, len(declared))
	for _, rule := range declared {
		out = append(out, resolve(rule))
	}
	return out
}

// validateResolvedConsistency enforces the composite contract on fully
// resolved rules: every anyOf/eitherOf operand gates exactly one tool. After
// resolution an operand's field is always set (empty took the declaring
// command's tool), so distinct fields here mean the composite spans two
// tools — a mismatch the declaration-time check cannot see when explicit and
// inherited fields mix. Without this, the consumer's composite constructors
// panic on the rendered manifest.
func validateResolvedConsistency(rules []*clicontract.Rule) error {
	var walk func(rule *clicontract.Rule) error
	walk = func(rule *clicontract.Rule) error {
		var operands []*clicontract.Rule
		switch rule.Type {
		case clicontract.RuleAnyOf:
			operands = rule.Rules
		case clicontract.RuleEitherOf:
			for _, branch := range rule.Branches {
				operands = append(operands, branch...)
			}
		default:
			return nil
		}
		seen := ""
		for _, inner := range operands {
			if err := walk(inner); err != nil {
				return err
			}
			if inner.Field == "" {
				continue
			}
			if seen == "" {
				seen = inner.Field
			} else if seen != inner.Field {
				return fmt.Errorf(
					"%s rules must target the same fields; resolved operands name %q and %q",
					rule.Type, seen, inner.Field,
				)
			}
		}
		return nil
	}
	for _, rule := range rules {
		if err := walk(rule); err != nil {
			return err
		}
	}
	return nil
}

// resolveTargets rewrites a disables rule's targets from command paths
// ("view") to tool names ("testcli-view"). A target that matches neither a
// full tool name nor a declared command path is an error.
func resolveTargets(appName string, tools map[string]toolShape, rules []*clicontract.Rule) ([]*clicontract.Rule, error) {
	resolveOne := func(target string) (string, bool) {
		if _, ok := tools[target]; ok {
			return target, true
		}
		full := clicontract.ToolName(appName, target)
		if _, ok := tools[full]; ok {
			return full, true
		}
		return "", false
	}
	var walk func(rule *clicontract.Rule) error
	walk = func(rule *clicontract.Rule) error {
		for i, target := range rule.Targets {
			resolved, ok := resolveOne(target)
			if !ok {
				return fmt.Errorf(
					"availability rule targets %q, which matches no declared command path or tool name",
					target,
				)
			}
			rule.Targets[i] = resolved
		}
		for _, inner := range rule.Rules {
			if err := walk(inner); err != nil {
				return err
			}
		}
		for _, branch := range rule.Branches {
			for _, inner := range branch {
				if err := walk(inner); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, rule := range rules {
		if err := walk(rule); err != nil {
			return nil, err
		}
	}
	return rules, nil
}

// buildTool renders one leaf command as a tool. Input rules render beside the
// input schema; a rule that names a flag the manifest omits is an error, so the
// generated block cannot dangle.
func buildTool(opts Options, timeout string, appName string, path []string, cmd *clicontract.Command, common []clicontract.Flag) (toolShape, error) {
	full := strings.Join(path, " ")
	command := append([]string{opts.Binary}, path...)
	if t, ok := opts.Timeouts[full]; ok {
		timeout = t
	}

	props := map[string]any{}
	var required []string
	var bindings []Binding

	// Positionals bind in order; the last one absorbs the rest when
	// variadic.
	for _, p := range cmd.Positionals {
		name := p.Name
		if p.Variadic {
			name += "_list"
			props[name] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		} else {
			props[name] = map[string]any{"type": "string"}
		}
		if p.Required {
			required = append(required, name)
		}
		bindings = append(bindings, Binding{Property: name, Style: "positional"})
	}

	for _, f := range cmd.Flags {
		if f.Required {
			required = append(required, f.Name)
		}
		switch f.Kind {
		case clicontract.Bool:
			props[f.Name] = map[string]any{"type": "boolean"}
			bindings = append(bindings, Binding{Property: f.Name, Style: "space", Flag: "--" + f.Name})
		case clicontract.Repeat:
			props[f.Name] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			bindings = append(bindings, Binding{Property: f.Name, Style: "equals", Flag: "--" + f.Name})
		default:
			props[f.Name] = map[string]any{"type": "string"}
			bindings = append(bindings, Binding{Property: f.Name, Style: "space", Flag: "--" + f.Name})
		}
	}
	for _, name := range opts.CommonFlags {
		for _, f := range common {
			if f.Name == name {
				props[f.Name] = map[string]any{"type": schemaKind(f.Kind)}
				bindings = append(bindings, Binding{Property: f.Name, Style: "space", Flag: "--" + f.Name})
				if f.Required {
					required = append(required, f.Name)
				}
			}
		}
	}
	if len(required) == 0 {
		required = nil
	}

	inputSchema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		sort.Strings(required)
		inputSchema["required"] = required
	}

	// Envelope-carrying CLIs report through their exit codes; the envelope
	// stays authoritative, so every documented code decodes.
	output := map[string]any{
		"mode":               "json",
		"success_exit_codes": []int{0, 1, 2, 3, 4},
	}

	if err := validateInputRuleSubjects(full, cmd.InputRules, props); err != nil {
		return toolShape{}, err
	}

	return toolShape{
		Description: cmd.Short,
		Command:     command,
		Timeout:     timeout,
		InputSchema: inputSchema,
		InputRules:  ruleShapesFrom(cmd.InputRules),
		Bindings:    bindings,
		Output:      output,
	}, nil
}

// ruleShapesFrom converts a declared rule list to its wire shape, skipping nil
// entries the startup validation rejects anyway.
func ruleShapesFrom(rules []*clicontract.Rule) []ruleShape {
	if len(rules) == 0 {
		return nil
	}
	out := make([]ruleShape, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		out = append(out, ruleShapeFrom(rule))
	}
	return out
}

// validateInputRuleSubjects checks a command's input rules against the
// properties the tool exposes. A rule naming a flag the manifest omits — a
// common flag outside Options.CommonFlags — would leave the consumer a rule it
// cannot evaluate, so it fails here instead.
func validateInputRuleSubjects(path string, rules []*clicontract.Rule, props map[string]any) error {
	check := func(flag string) error {
		if _, ok := props[flag]; ok {
			return nil
		}
		return fmt.Errorf(
			"command %q input rule names %q, which this manifest does not expose as a property; "+
				"add it to Options.CommonFlags or drop the rule",
			path, flag,
		)
	}
	var walkExpr func(*clicontract.Expr) error
	walkExpr = func(expr *clicontract.Expr) error {
		if expr == nil {
			return nil
		}
		if expr.Field != "" {
			if err := check(expr.Field); err != nil {
				return err
			}
		}
		for _, inner := range expr.Exprs {
			if err := walkExpr(inner); err != nil {
				return err
			}
		}
		return walkExpr(expr.Expr)
	}
	var walkRule func(*clicontract.Rule) error
	walkRule = func(rule *clicontract.Rule) error {
		if rule == nil {
			return nil
		}
		// Manifest is a direct entry point, so it repeats the declaration
		// checks it depends on: a gate rule with no subject would render an
		// input rule that constrains nothing.
		if (rule.Type == clicontract.RuleEnabledWhen || rule.Type == clicontract.RuleRequires) && rule.Field == "" {
			return fmt.Errorf(
				"command %q input rule (%s) has no field; an input rule names the flag it constrains",
				path, rule.Type,
			)
		}
		if rule.Field != "" {
			if err := check(rule.Field); err != nil {
				return err
			}
		}
		for _, target := range rule.Targets {
			if err := check(target); err != nil {
				return err
			}
		}
		if err := walkExpr(rule.When); err != nil {
			return err
		}
		for _, inner := range rule.Rules {
			if err := walkRule(inner); err != nil {
				return err
			}
		}
		for _, branch := range rule.Branches {
			for _, inner := range branch {
				if err := walkRule(inner); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, rule := range rules {
		if err := walkRule(rule); err != nil {
			return err
		}
	}
	return nil
}

// schemaKind names the JSON Schema type for one flag kind.
func schemaKind(k clicontract.Kind) string {
	switch k {
	case clicontract.Bool:
		return "boolean"
	case clicontract.Repeat:
		return "array"
	case clicontract.Int:
		return "integer"
	default:
		// Duration flags bind as their Go duration string.
		return "string"
	}
}
