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
	"time"

	"github.com/spf13/cobra"
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
	// Int is an integer-valued flag.
	Int
	// Duration is a time.Duration flag parsed from a Go duration string.
	Duration
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
	// Short is the optional single-letter shorthand (for example "f" for
	// --follow). Empty means none.
	Short string
	// Required marks a flag the caller must provide. Rendered as required
	// in the schema document and in generated manifests.
	Required bool
}

// Positional declares one positional argument. Required positionals render
// as <name> in help and must be present; optional ones render as [name]. A
// variadic positional (the last one) absorbs any remaining arguments.
type Positional struct {
	Name     string
	Required bool
	Variadic bool
}

// Invocation carries one parsed command invocation.
type Invocation struct {
	Command string
	// Sub is the nested subcommand for commands that have one; empty
	// otherwise.
	Sub string
	// Args holds the positional values in declaration order.
	Args []string
	// Flags holds single-valued flag values by name; absent flags are "".
	Flags map[string]string
	// Bools holds boolean flags by name; absent flags are false.
	Bools map[string]bool
	// Events holds repeated flag values (kind Repeat).
	Events []string
	// Ints holds integer flag values by name; absent flags are 0.
	Ints map[string]int
	// Durations holds duration flag values by name; absent flags are 0.
	Durations map[string]time.Duration
}

// HandleFunc runs one parsed command invocation. The returned result is
// app-defined; the adapter writes it and derives the exit code. Stderr is
// the diagnostics writer for handler progress.
type HandleFunc func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error)

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
	// Positionals are the command's positional arguments, in order.
	Positionals []Positional
	// NoInterspersed stops flag parsing at the first positional, so dashed
	// values meant for the command pass through instead of parsing as
	// flags. The default (false) allows flags anywhere.
	NoInterspersed bool
	// Flags are the command-specific flags. Common flags are declared once
	// on the App and registered persistently.
	Flags []Flag
	// PersistentFlags are flags declared on a parent command and inherited
	// by its subcommands; they parse before the subcommand name. Only
	// meaningful on commands that own Subcommands.
	PersistentFlags []Flag
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
	// ErrorPrefix is the optional prefix on dispatch-error diagnostics:
	// "mycli plan: unknown flag". Empty means the command path alone.
	ErrorPrefix string
	// BareHelp makes a bare invocation print help and exit 0 instead of
	// failing with a missing-subcommand caller error.
	BareHelp bool
	// HandlerOwnedSignals stops the library from watching SIGINT/SIGTERM:
	// handlers that run servers or manage their own shutdown install
	// their own handlers and choose the exit code. The default (false)
	// exits with the signal's shell code (130/143) and suppresses stdout.
	HandlerOwnedSignals bool
	// VersionTemplate renders the --version output; the default is
	// "<name> {{.Version}}\n".
	VersionTemplate string
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

// validate checks one declared App: every command and subcommand has a
// short, flags are well-formed, and Handle/Raw are exclusive. Called by Run
// so drift fails at startup.
func validate(app *App) error {
	if err := validateFlags(app.Name, app.CommonFlags); err != nil {
		return err
	}
	for _, cmd := range app.Commands {
		if cmd.Short == "" {
			return UsageErrorf("command %q has no help short", cmd.Name)
		}
		if len(cmd.Subcommands) == 0 {
			// Only leaf commands run: a parent with subcommands fails with
			// a caller error when invoked bare.
			if (cmd.Handle == nil) == (cmd.Raw == nil) {
				return UsageErrorf("command %q must declare exactly one of Handle or Raw", cmd.Name)
			}
		}
		if err := validateFlags(cmd.Name, cmd.Flags); err != nil {
			return err
		}
		for _, sub := range cmd.Subcommands {
			if sub.Short == "" {
				return UsageErrorf("command %q subcommand %q has no help short", cmd.Name, sub.Name)
			}
			if (sub.Handle == nil) == (sub.Raw == nil) {
				return UsageErrorf("command %q subcommand %q must declare exactly one of Handle or Raw", cmd.Name, sub.Name)
			}
			if err := validateFlags(cmd.Name+" "+sub.Name, sub.Flags); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateFlags checks one flag list: named, known kind, and a usage string.
func validateFlags(command string, flags []Flag) error {
	for _, f := range flags {
		switch f.Kind {
		case String, Bool, Repeat, Int, Duration:
		default:
			return UsageErrorf("command %q flag %q has unknown kind %d", command, f.Name, f.Kind)
		}
		if f.Usage == "" {
			return UsageErrorf("command %q flag %q has no usage string", command, f.Name)
		}
		if f.Name == "" {
			return UsageErrorf("command %q has an unnamed flag", command)
		}
	}
	return nil
}

// commandUse is the cobra Use string for one command: the name plus the
// positionals, required as <name>, optional as [name], variadic as
// <name>... or [name...].
func commandUse(cmd *Command) string {
	use := cmd.Name
	for _, p := range cmd.Positionals {
		switch {
		case p.Required && p.Variadic:
			use += " <" + p.Name + ">..."
		case p.Required:
			use += " <" + p.Name + ">"
		case p.Variadic:
			use += " [" + p.Name + "...]"
		default:
			use += " [" + p.Name + "]"
		}
	}
	return use
}

// errf formats a caller error under the app's error prefix and command
// path: "mycli plan: unexpected argument". The bare path is used when no
// prefix is declared.
func (a *App) errf(path, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if a.ErrorPrefix == "" {
		return UsageErrorf("%s: %s", path, msg)
	}
	if path == a.Name {
		return UsageErrorf("%s: %s", a.ErrorPrefix, msg)
	}
	return UsageErrorf("%s %s: %s", a.ErrorPrefix, path, msg)
}

// positionalArgs validates the positional count against the declaration.
func (a *App) positionalArgs(path string, cmd *Command) cobra.PositionalArgs {
	required := 0
	variadic := false
	for i, p := range cmd.Positionals {
		if p.Required {
			required = i + 1
		}
		if p.Variadic {
			variadic = true
		}
	}
	return func(c *cobra.Command, args []string) error {
		switch {
		case len(args) < required:
			missing := cmd.Positionals[len(args)].Name
			return a.errf(path, "missing <%s>", missing)
		case !variadic && len(args) > len(cmd.Positionals):
			return a.errf(path, "unexpected argument %q", args[len(cmd.Positionals)])
		}
		return nil
	}
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
