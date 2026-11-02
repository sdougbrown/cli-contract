package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	clicontract "github.com/sdougbrown/cli-contract"
)

// Declaration mirrors one v1 declaration document.
type Declaration struct {
	Version     int                `json:"version"`
	Name        string             `json:"name"`
	AppVersion  string             `json:"appVersion"`
	ErrorPrefix string             `json:"errorPrefix,omitempty"`
	Behavior    Behavior           `json:"behavior,omitempty"`
	CommonFlags map[string]Flag    `json:"commonFlags,omitempty"`
	Commands    map[string]Command `json:"commands"`
}

// Behavior is the app-declared behavior half of the document.
type Behavior struct {
	BareHelp            bool              `json:"bareHelp,omitempty"`
	HandlerOwnedSignals bool              `json:"handlerOwnedSignals,omitempty"`
	ExitCodes           map[string]string `json:"exitCodes,omitempty"`
}

// Flag is one declared flag.
type Flag struct {
	Kind     string `json:"kind"`
	Usage    string `json:"usage"`
	Default  any    `json:"default,omitempty"`
	Required bool   `json:"required,omitempty"`
	Short    string `json:"short,omitempty"`
}

// Positional is one declared positional.
type Positional struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Variadic bool   `json:"variadic,omitempty"`
}

// Command is one declared command.
type Command struct {
	Short          string             `json:"short"`
	Positionals    []Positional       `json:"positionals,omitempty"`
	Output         string             `json:"output,omitempty"`
	NoInterspersed bool               `json:"noInterspersed,omitempty"`
	Flags          map[string]Flag    `json:"flags,omitempty"`
	Subcommands    map[string]Command `json:"subcommands,omitempty"`
}

// BuildApp maps one declaration onto a clicontract.App and the fixture
// Adapter that writes the handler-contract envelope. The handlers are the
// fixture handlers: they echo the command path, status, parsed flags, and
// positionals; the env command adds the FIXTURE_MARKER value; the own
// command exits with the declared code 3 on --conflict; the sleep command
// returns immediately in-process (the injected signal stands in for the
// signal that would arrive while it stays alive).
func BuildApp(d *Declaration) (*clicontract.App, *Adapter) {
	adapter := &Adapter{}
	app := &clicontract.App{
		Name:                d.Name,
		Version:             d.AppVersion,
		ErrorPrefix:         d.ErrorPrefix,
		BareHelp:            d.Behavior.BareHelp,
		HandlerOwnedSignals: d.Behavior.HandlerOwnedSignals,
		CommonFlags:         flagsOf(d.CommonFlags),
	}
	for _, name := range sortedNames(d.Commands) {
		app.Commands = append(app.Commands, commandOf(name, d.Commands[name], nil, d.CommonFlags))
	}
	return app, adapter
}

// ancestor is one declared command above a leaf: its name for the echo
// path, and its persistent flags for the echo set.
type ancestor struct {
	name  string
	flags map[string]Flag
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// flagsOf renders a flag map as a sorted flag list.
func flagsOf(flags map[string]Flag) []clicontract.Flag {
	out := make([]clicontract.Flag, 0, len(flags))
	for _, name := range sortedNames(flags) {
		out = append(out, flagOf(name, flags[name]))
	}
	return out
}

func flagOf(name string, f Flag) clicontract.Flag {
	return clicontract.Flag{
		Name:     name,
		Kind:     kindOf(f.Kind),
		Usage:    f.Usage,
		Default:  defaultOf(f),
		Required: f.Required,
		Short:    f.Short,
	}
}

func kindOf(kind string) clicontract.Kind {
	switch kind {
	case "bool":
		return clicontract.Bool
	case "repeat":
		return clicontract.Repeat
	case "integer":
		return clicontract.Int
	case "duration":
		return clicontract.Duration
	default:
		return clicontract.String
	}
}

// defaultOf converts a JSON default into the declared Go value the
// library applies at parse time.
func defaultOf(f Flag) any {
	switch f.Kind {
	case "integer":
		if n, ok := f.Default.(float64); ok {
			return int(n)
		}
	case "duration":
		if s, ok := f.Default.(string); ok {
			if d, err := time.ParseDuration(s); err == nil {
				return d
			}
		}
	case "repeat":
		if items, ok := f.Default.([]any); ok {
			out := make([]string, 0, len(items))
			for _, item := range items {
				out = append(out, fmt.Sprint(item))
			}
			return out
		}
	default:
		return f.Default
	}
	return nil
}

// commandOf renders one declared command (recursively) as a Command. The
// ancestors carry the names and persistent flag maps declared above it, so
// a leaf echoes its full path and every flag its invocation parses.
func commandOf(name string, c Command, ancestors []ancestor, common map[string]Flag) *clicontract.Command {
	out := &clicontract.Command{
		Name:           name,
		Short:          c.Short,
		NoInterspersed: c.NoInterspersed,
		Flags:          flagsOf(c.Flags),
	}
	for _, p := range c.Positionals {
		out.Positionals = append(out.Positionals, clicontract.Positional{
			Name: p.Name, Required: p.Required, Variadic: p.Variadic,
		})
	}
	if len(c.Subcommands) > 0 {
		// The parent's declared flags are the persistent flags its
		// subcommands inherit (spec §4); the subcommands echo them.
		out.PersistentFlags = flagsOf(c.Flags)
		out.Flags = nil
		below := append(append([]ancestor{}, ancestors...), ancestor{name: name, flags: c.Flags})
		for _, sub := range sortedNames(c.Subcommands) {
			out.Subcommands = append(out.Subcommands, commandOf(sub, c.Subcommands[sub], below, common))
		}
		return out
	}
	output := c.Output
	if output == "" {
		output = "json-value"
	}
	if output == "raw-text" {
		out.Raw = func(ctx context.Context, inv clicontract.Invocation, stdout io.Writer) error {
			fmt.Fprintln(stdout, "canned output")
			return nil
		}
		return out
	}
	// json-value and none are Handle commands: json-value returns the
	// envelope the adapter writes, none returns nil and the adapter writes
	// nothing. The sleep command stays alive in a real binary; in-process
	// it returns immediately and the injected signal stands in for the
	// signal that would arrive while it runs.
	path := pathOf(name, ancestors)
	forceNil := output == "none"
	echo := echoSpecs(c.Flags, ancestors, common)
	out.Handle = func(ctx context.Context, inv clicontract.Invocation, stderr io.Writer) (any, error) {
		if forceNil {
			return nil, nil
		}
		return handlerResult(path, echo, inv), nil
	}
	return out
}

// pathOf joins the leaf's full command path space-separated.
func pathOf(name string, ancestors []ancestor) string {
	parts := make([]string, 0, len(ancestors)+1)
	for _, a := range ancestors {
		parts = append(parts, a.name)
	}
	parts = append(parts, name)
	return strings.Join(parts, " ")
}

// echoSpecs collects the kinds a leaf echoes: the app's common flags,
// every ancestor's persistent flags, and its own flags.
func echoSpecs(own map[string]Flag, ancestors []ancestor, common map[string]Flag) map[string]Flag {
	specs := map[string]Flag{}
	sources := []map[string]Flag{common}
	for _, a := range ancestors {
		sources = append(sources, a.flags)
	}
	sources = append(sources, own)
	for _, source := range sources {
		for name, f := range source {
			specs[name] = f
		}
	}
	return specs
}

// handlerResult builds the handler-contract envelope for one invocation.
func handlerResult(path string, echo map[string]Flag, inv clicontract.Invocation) any {
	if path == "env" {
		marker := os.Getenv("FIXTURE_MARKER")
		return map[string]any{"command": path, "status": "ok", "marker": marker}
	}
	result := map[string]any{
		"command": path,
		"status":  "ok",
		"args":    append([]string{}, inv.Args...),
	}
	for _, name := range sortedNames(echo) {
		echoFlag(result, name, echo[name].Kind, inv)
	}
	if path == "own" && inv.Bools["conflict"] {
		result["__exit"] = 3
	}
	return result
}

// echoFlag echoes one parsed flag value under its name, kind-shaped.
func echoFlag(result map[string]any, name, kind string, inv clicontract.Invocation) {
	switch kind {
	case "bool":
		result[name] = inv.Bools[name]
	case "integer":
		result[name] = inv.Ints[name]
	case "duration":
		result[name] = inv.Durations[name].String()
	case "repeat":
		result[name] = append([]string{}, inv.Events...)
	default:
		result[name] = inv.Flags[name]
	}
}

// Adapter writes the fixture envelope and derives the fixture exit codes.
// It implements TestSeams so the signal cases inject their signal into the
// library's signal path.
type Adapter struct {
	signal string
}

func (a *Adapter) ErrorResult(command string, err error) any {
	code := "internal"
	if _, ok := err.(*clicontract.UsageError); ok {
		code = "usage"
	}
	return map[string]any{
		"command": command,
		"status":  "error",
		"error":   map[string]any{"code": code, "message": err.Error()},
	}
}

func (a *Adapter) Write(w io.Writer, result any) error {
	if result == nil {
		return nil
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func (a *Adapter) ExitCode(inv clicontract.Invocation, result any, handlerErr error) int {
	if handlerErr != nil {
		return 2
	}
	if env, ok := result.(map[string]any); ok {
		if exit, ok := env["__exit"].(int); ok {
			return exit
		}
	}
	return 0
}

func (a *Adapter) Deadline(ctx context.Context, inv clicontract.Invocation) (context.Context, context.CancelFunc) {
	return ctx, func() {}
}

func (a *Adapter) BeforeHandler(inv clicontract.Invocation, stderr io.Writer) {}

func (a *Adapter) Validate(inv clicontract.Invocation) error { return nil }

// BeforeWrite and PendingSignal implement clicontract.TestSeams: the
// pending signal is the case's signal, injected where the library checks
// for one around the envelope write.
func (a *Adapter) BeforeWrite() {}

func (a *Adapter) PendingSignal() (os.Signal, bool) {
	switch a.signal {
	case "SIGINT":
		return os.Interrupt, true
	case "SIGTERM":
		return syscall.SIGTERM, true
	}
	return nil, false
}
