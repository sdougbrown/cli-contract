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
	Version int                  `json:"version"`
	Tools   map[string]toolShape `json:"tools"`
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
func Manifest(app *clicontract.App, opts Options) ([]byte, error) {
	timeout := opts.DefaultTimeout
	if timeout == "" {
		timeout = DefaultTimeout
	}
	tools := map[string]toolShape{}
	for _, cmd := range app.Commands {
		if len(cmd.Subcommands) > 0 {
			for _, sub := range cmd.Subcommands {
				tools[toolName(app.Name, cmd.Name+"-"+sub.Name)] = buildTool(
					opts, timeout, app.Name, []string{cmd.Name, sub.Name}, sub, app.CommonFlags)
			}
			continue
		}
		tools[toolName(app.Name, cmd.Name)] = buildTool(
			opts, timeout, app.Name, []string{cmd.Name}, cmd, app.CommonFlags)
	}
	for name, t := range tools {
		bindings := t.Bindings
		sort.Slice(bindings, func(i, j int) bool {
			return bindings[i].Property < bindings[j].Property
		})
		t.Bindings = bindings
		tools[name] = t
	}
	b, err := json.MarshalIndent(manifestShape{Version: 1, Tools: tools}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
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
