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
// the declared conditions and the rules that gate tools.
type availabilityShape struct {
	Conditions map[string]conditionShape       `json:"conditions,omitempty"`
	Rules      []*clicontract.AvailabilityRule `json:"rules,omitempty"`
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
	var rules []*clicontract.AvailabilityRule
	var walk func(path []string, cmd *clicontract.Command)
	walk = func(path []string, cmd *clicontract.Command) {
		if len(cmd.Subcommands) > 0 {
			for _, sub := range cmd.Subcommands {
				walk(append(path, sub.Name), sub)
			}
			return
		}
		tool := toolName(app.Name, strings.Join(path, "-"))
		tools[tool] = buildTool(
			opts, timeout, app.Name, path, cmd, app.CommonFlags)
		rules = append(rules, resolvedRules(tool, cmd.Availability)...)
	}
	for _, cmd := range app.Commands {
		walk([]string{cmd.Name}, cmd)
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
	var availability *availabilityShape
	if len(app.AvailabilityConditions) > 0 || len(rules) > 0 {
		conditions := make(map[string]conditionShape, len(app.AvailabilityConditions))
		for _, c := range app.AvailabilityConditions {
			conditions[c.Name] = conditionShape{Type: c.Type, Description: c.Description}
		}
		availability = &availabilityShape{Conditions: conditions, Rules: rules}
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
func resolvedRules(tool string, declared []*clicontract.AvailabilityRule) []*clicontract.AvailabilityRule {
	if len(declared) == 0 {
		return nil
	}
	var resolve func(rule *clicontract.AvailabilityRule) *clicontract.AvailabilityRule
	resolve = func(rule *clicontract.AvailabilityRule) *clicontract.AvailabilityRule {
		out := *rule
		// Only the field-targeting leaf forms carry a field; disables names
		// its tools through targets, and composite wrappers stay bare.
		if out.Field == "" && (out.Type == "enabledWhen" || out.Type == "requires") {
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
			out.Branches = make(map[string][]*clicontract.AvailabilityRule, len(rule.Branches))
			for name, branch := range rule.Branches {
				resolvedBranch := make([]*clicontract.AvailabilityRule, 0, len(branch))
				for _, inner := range branch {
					resolvedBranch = append(resolvedBranch, resolve(inner))
				}
				out.Branches[name] = resolvedBranch
			}
		}
		return &out
	}
	out := make([]*clicontract.AvailabilityRule, 0, len(declared))
	for _, rule := range declared {
		out = append(out, resolve(rule))
	}
	return out
}

// resolveTargets rewrites a disables rule's targets from command paths
// ("view") to tool names ("testcli-view"). A target that matches neither a
// full tool name nor a declared command path is an error.
func resolveTargets(appName string, tools map[string]toolShape, rules []*clicontract.AvailabilityRule) ([]*clicontract.AvailabilityRule, error) {
	resolveOne := func(target string) (string, bool) {
		if _, ok := tools[target]; ok {
			return target, true
		}
		full := toolName(appName, target)
		if _, ok := tools[full]; ok {
			return full, true
		}
		return "", false
	}
	var walk func(rule *clicontract.AvailabilityRule) error
	walk = func(rule *clicontract.AvailabilityRule) error {
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

// toolName joins the command path with dashes.
func toolName(prefix, name string) string {
	return strings.ReplaceAll(prefix+" "+name, " ", "-")
}

// buildTool renders one leaf command as a tool.
func buildTool(opts Options, timeout string, appName string, path []string, cmd *clicontract.Command, common []clicontract.Flag) toolShape {
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

	return toolShape{
		Description: cmd.Short,
		Command:     command,
		Timeout:     timeout,
		InputSchema: inputSchema,
		Bindings:    bindings,
		Output:      output,
	}
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
