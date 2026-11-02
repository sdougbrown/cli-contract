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
	// Path holds the nested command names below the root command, leaf
	// included ("controller", "status" for `app workflow controller
	// status`). Nil for top-level commands.
	Path []string
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
	// Supplied marks the flags given on the command line (pflag's
	// Changed): an omitted flag — even one with a declared default — is
	// absent from the map. Covers every kind, including inherited
	// persistent and common flags.
	Supplied map[string]bool
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
// short, flags are well-formed, shorthands are unique within every merged
// flag set cobra builds, and Handle/Raw are exclusive. Called by Run and
// Parse so drift fails at startup.
func validate(app *App) error {
	common := app.CommonFlags
	if err := validateFlags(app.Name, common, declaresHelp(common)); err != nil {
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
		if err := validateFlags(cmd.Name, cmd.Flags, declaresHelp(common, cmd.PersistentFlags, cmd.Flags)); err != nil {
			return err
		}
		if err := validateFlags(cmd.Name, cmd.PersistentFlags, declaresHelp(common, cmd.PersistentFlags)); err != nil {
			return err
		}
		inheritedHelp := declaresHelp(common, cmd.PersistentFlags)
		for _, sub := range cmd.Subcommands {
			if err := validateNested(cmd.Name, sub, inheritedHelp); err != nil {
				return err
			}
		}
	}
	return validateShorthands(app)
}

// declaresHelp reports whether one of these flag lists declares a flag named
// "help": a merged flag set that has one keeps cobra from claiming the "h"
// shorthand for its own help flag.
func declaresHelp(lists ...[]Flag) bool {
	for _, flags := range lists {
		for _, f := range flags {
			if f.Name == "help" {
				return true
			}
		}
	}
	return false
}

// validateShorthands rejects shorthands that repeat within any merged flag
// set cobra builds for a node: the common flags, the persistent flags
// along its path, and the node's own flags. pflag panics at registration
// on a repeated shorthand; startup validation keeps the failure a
// UsageError. A flag that shadows a same-named inherited one keeps its
// shorthand: cobra defers to the command's own flag then.
func validateShorthands(app *App) error {
	// The common flags are the surface every command inherits.
	common := map[string]string{}
	for _, f := range app.CommonFlags {
		if f.Short == "" {
			continue
		}
		if prev, ok := common[f.Short]; ok && prev != f.Name {
			return UsageErrorf("common flags %q and %q share the %q shorthand", prev, f.Name, f.Short)
		}
		common[f.Short] = f.Name
	}
	// claim records one flag list's shorthands in surface, rejecting a
	// shorthand another flag on the same merged set already holds.
	claim := func(path string, flags []Flag, surface map[string]string) error {
		for _, f := range flags {
			if f.Short == "" {
				continue
			}
			if prev, ok := surface[f.Short]; ok && prev != f.Name {
				return UsageErrorf("command %q flags %q and %q share the %q shorthand", path, prev, f.Name, f.Short)
			}
			surface[f.Short] = f.Name
		}
		return nil
	}
	// walk descends the declared tree carrying the inherited surface.
	// Persistent flags join the surface every descendant sees; a node's
	// own flags collide against the surface but do not propagate.
	var walk func(path string, c *Command, inherited map[string]string) error
	walk = func(path string, c *Command, inherited map[string]string) error {
		surface := make(map[string]string, len(inherited)+len(c.PersistentFlags))
		for k, v := range inherited {
			surface[k] = v
		}
		if err := claim(path, c.PersistentFlags, surface); err != nil {
			return err
		}
		own := make(map[string]string, len(surface)+len(c.Flags))
		for k, v := range surface {
			own[k] = v
		}
		if err := claim(path, c.Flags, own); err != nil {
			return err
		}
		for _, sub := range c.Subcommands {
			if err := walk(path+" "+sub.Name, sub, surface); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cmd := range app.Commands {
		if err := walk(cmd.Name, cmd, common); err != nil {
			return err
		}
	}
	return nil
}

// validateFlags checks one flag list: named, known kind, a usage string,
// and a one-letter shorthand cobra can register without claiming the help
// flag's. helpDeclared reports whether the merged flag set the list belongs
// to already declares a flag named "help", which leaves "h" free.
// validateNested checks one nested node recursively: help text and
// well-formed flags on every node, and exactly one of Handle or Raw on
// leaves only. inheritedHelp carries whether the flags the node inherits
// from the common flags and its ancestors' persistent flags declare "help".
func validateNested(parent string, sub *Command, inheritedHelp bool) error {
	path := parent + " " + sub.Name
	if sub.Short == "" {
		return UsageErrorf("command %q has no help short", path)
	}
	if err := validateFlags(path, sub.Flags, inheritedHelp || declaresHelp(sub.PersistentFlags, sub.Flags)); err != nil {
		return err
	}
	if err := validateFlags(path, sub.PersistentFlags, inheritedHelp || declaresHelp(sub.PersistentFlags)); err != nil {
		return err
	}
	if len(sub.Subcommands) > 0 {
		below := inheritedHelp || declaresHelp(sub.PersistentFlags)
		for _, inner := range sub.Subcommands {
			if err := validateNested(path, inner, below); err != nil {
				return err
			}
		}
		return nil
	}
	if (sub.Handle == nil) == (sub.Raw == nil) {
		return UsageErrorf("command %q must declare exactly one of Handle or Raw", path)
	}
	return nil
}

func validateFlags(command string, flags []Flag, helpDeclared bool) error {
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
		if f.Short != "" && len(f.Short) != 1 {
			return UsageErrorf("command %q flag %q uses shorthand %q, want one letter", command, f.Name, f.Short)
		}
		// Cobra's default help flag claims the "h" shorthand on every
		// command whose merged flag set has no flag named "help", during
		// execute; a declaration using it would panic there.
		if f.Short == "h" && !helpDeclared {
			return UsageErrorf("command %q flag %q uses shorthand %q, reserved for the help flag", command, f.Name, f.Short)
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
