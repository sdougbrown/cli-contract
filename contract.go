// Package clicontract turns one declaration of a CLI's command surface into
// everything downstream consumers read: the cobra command tree, the
// machine-readable schema document, and the run orchestration that routes
// caller errors and exit codes.
//
// Declare commands once; the library derives the tree and the schema from
// the same declarations, and cross-checks them so the two surfaces cannot
// drift. The app supplies its own result vocabulary through an Adapter: the
// library never formats envelopes or chooses exit codes beyond the generic
// conventions (help and version exit 0, signals win, caller errors route
// through the adapter).
package clicontract

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Kind is the value kind of one flag.
type Kind int

const (
	// String is a single-valued flag; absent means the zero value.
	String Kind = iota
	// Bool is a boolean flag.
	Bool
	// Repeat collects repeated values under one flag.
	Repeat
)

// Flag declares one flag of one command. Usage is rendered in help and in
// the schema document; the same declaration feeds both surfaces.
type Flag struct {
	Name string
	Kind Kind
	// Usage is the flag's documented behavior. It may carry the documented
	// effective default inline (for example "maximum events (default 50)").
	Usage string
	// Default is the documented effective default the handlers apply when
	// the flag is absent. Nil means the flag's zero value.
	Default any
}

// Invocation carries one parsed command invocation.
type Invocation struct {
	Command string
	// Sub is the nested subcommand for commands that have one; empty
	// otherwise.
	Sub string
	URL string
	// Flags holds single-valued flag values by name; absent flags are "".
	Flags map[string]string
	// Bools holds boolean flags by name; absent flags are false.
	Bools map[string]bool
	// Events holds repeated flag values (kind Repeat).
	Events []string
}

// HandleFunc runs one parsed command invocation. The returned result is
// app-defined; the adapter writes it and derives the exit code.
type HandleFunc func(ctx context.Context, inv Invocation) (any, error)

// RawFunc writes directly to stdout instead of returning a result: the
// command's output is not an envelope (help text, a schema document). A raw
// command exits 0 with nothing else written; an error returns through the
// dispatch error path.
type RawFunc func(ctx context.Context, inv Invocation, stdout io.Writer) error

// Command declares one command of the CLI.
type Command struct {
	Name  string
	Short string
	Long  string
	// URL declares that the command takes exactly one URL positional.
	URL bool
	// Flags are the command-specific flags. Common flags are declared once
	// on the App and registered persistently.
	Flags []Flag
	// Subcommands nest under this command (store path, store reset).
	Subcommands []*Command
	// Handle runs the command and returns the result the adapter writes.
	// Mutually exclusive with Raw.
	Handle HandleFunc
	// Raw writes directly to stdout. Mutually exclusive with Handle.
	Raw RawFunc
}

// UsageError marks a caller error: the adapter typically routes it to the
// usage exit code with a usage-classified envelope.
type UsageError struct{ Message string }

func (e *UsageError) Error() string { return e.Message }

// App is the declared CLI: the command surface, the common flags, and the
// version string the root --version flag reports.
type App struct {
	Name    string
	Version string
	// RootShort and RootLong are the bare help's short and long text.
	RootShort   string
	RootLong    string
	Commands    []*Command
	CommonFlags []Flag
}

// UsageErrorf builds a UsageError with a formatted message.
func UsageErrorf(format string, args ...any) *UsageError {
	return &UsageError{Message: fmt.Sprintf(format, args...)}
}

// validate checks one declaration set: every command has a short, and flag
// kinds are known. Called by BuildTree so drift fails at startup.
func validate(commands []*Command) error {
	for _, cmd := range commands {
		if cmd.Short == "" {
			return UsageErrorf("command %q has no help short", cmd.Name)
		}
		for _, f := range cmd.Flags {
			switch f.Kind {
			case String, Bool, Repeat:
			default:
				return UsageErrorf("command %q flag %q has unknown kind %d", cmd.Name, f.Name, f.Kind)
			}
			if f.Usage == "" {
				return UsageErrorf("command %q flag %q has no usage string", cmd.Name, f.Name)
			}
			if f.Name == "" {
				return UsageErrorf("command %q has an unnamed flag", cmd.Name)
			}
		}
		if len(cmd.Subcommands) == 0 {
			// Only leaf commands run: a parent with subcommands fails with
			// a caller error when invoked bare.
			if (cmd.Handle == nil) == (cmd.Raw == nil) {
				return UsageErrorf("command %q must declare exactly one of Handle or Raw", cmd.Name)
			}
		}
		for _, sub := range cmd.Subcommands {
			if sub.Short == "" {
				return UsageErrorf("command %q subcommand %q has no help short", cmd.Name, sub.Name)
			}
			if (sub.Handle == nil) == (sub.Raw == nil) {
				return UsageErrorf("command %q subcommand %q must declare exactly one of Handle or Raw", cmd.Name, sub.Name)
			}
		}
	}
	return nil
}

// commandUse is the cobra Use string for one command.
func commandUse(cmd *Command) string {
	if cmd.URL {
		return cmd.Name + " <URL>"
	}
	return cmd.Name
}

// flagNames reports whether the command declares a flag with the given name.
func (cmd *Command) flagNames() map[string]bool {
	names := make(map[string]bool, len(cmd.Flags))
	for _, f := range cmd.Flags {
		names[f.Name] = true
	}
	return names
}

// find resolves argv[0] against the declared commands. Nested names resolve
// as "parent sub" (for example "store reset").
func (a *App) find(name string) *Command {
	parts := strings.SplitN(name, " ", 2)
	for _, cmd := range a.Commands {
		if cmd.Name != parts[0] {
			continue
		}
		if len(parts) == 1 {
			return cmd
		}
		for _, sub := range cmd.Subcommands {
			if sub.Name == parts[1] {
				return sub
			}
		}
		return nil
	}
	return nil
}
