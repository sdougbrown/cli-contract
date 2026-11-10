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
	"math"
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
	// Input rules compare its canonical spelling: the caller's input
	// normalizes (0m evaluates as 0s), so a rule value is written in
	// that form.
	Duration
	// DurationMS is a time.Duration flag whose value spells whole-number
	// milliseconds on the command line, in the schema document, and in
	// the rules: --wait 1500 is 1.5s. Rules compare JSON numbers, and a
	// consumer needs no normalization to evaluate them — the reason to
	// prefer this kind when non-Go tools read the contract. The handler
	// binds a time.Duration either way.
	DurationMS
)

// maxDurationMS bounds a duration_ms value: the milliseconds-to-Duration
// product wraps past it, so the declaration, the parser, and the rule-value
// validation all enforce this one bound.
const maxDurationMS = math.MaxInt64 / int64(time.Millisecond)

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
	// Required marks a flag the caller must provide. Rendered as required in
	// the schema document and in generated manifests, and enforced at parse
	// time.
	Required bool
}

// Positional declares one positional argument. Required positionals render
// as <name> in help and must be present; optional ones render as [name]. A
// variadic positional (the last one) absorbs any remaining arguments.
type Positional struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Variadic bool   `json:"variadic,omitempty"`
}

// OutputMode names how one command writes stdout: one JSON value, raw bytes,
// or nothing. "json" and "raw" match the cli-registry manifest's output.mode;
// the registry bridge translates "none" (nothing to decode) and a pointered
// JSON value ("json-pointer") into the manifest's vocabulary.
type OutputMode string

const (
	// OutputJSON writes exactly one JSON value on stdout.
	OutputJSON OutputMode = "json"
	// OutputRaw writes bytes the consumer does not decode as JSON.
	OutputRaw OutputMode = "raw"
	// OutputNone writes nothing; the exit code is the whole result.
	OutputNone OutputMode = "none"
)

// Output declares how one command's output is decoded and which exit codes
// mean success. The zero value declares nothing. Mode is required once any
// other member is set, a mode requires SuccessExitCodes, and only a leaf
// command may declare it.
type Output struct {
	Mode OutputMode `json:"mode"`
	// JSONPointer is the location of the command's result inside the JSON
	// value, for consumers that present a field rather than the envelope.
	// Empty means the value itself is the result.
	JSONPointer string `json:"json_pointer,omitempty"`
	// SuccessExitCodes lists the exit codes that mean success. A declared
	// mode requires an explicit list, so the published contract is the one
	// the author stated, never a default the renderer invented.
	SuccessExitCodes []int `json:"success_exit_codes,omitempty"`
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
	// Events holds repeated flag values (kind Repeat); with more than one
	// Repeat flag it holds whichever was read last, so read Repeated instead.
	Events []string
	// Repeated holds repeated flag values by name, one entry per declared
	// Repeat flag, so a command with two of them does not share one slice.
	Repeated map[string][]string
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

// StreamFunc writes its output incrementally and returns an adapter-defined
// result and error for exit classification. The adapter does not write the result.
type StreamFunc func(ctx context.Context, inv Invocation, stdout, stderr io.Writer) (any, error)

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
	// Flags are the command-specific flags; the App's CommonFlags are
	// registered on every command. Ignored on a command that owns
	// Subcommands, whose own flags are its PersistentFlags.
	Flags []Flag
	// PersistentFlags are flags declared on a parent command and inherited
	// by its subcommands; they parse before the subcommand name. Ignored on
	// a command without Subcommands.
	PersistentFlags []Flag
	// Subcommands nest under this command (store path, store reset).
	Subcommands []*Command
	// Availability gates the command's tool in generated registry manifests:
	// rules over the App's AvailabilityConditions. Only leaf commands can
	// gate; a command with subcommands fails validation when it declares
	// rules. Rules without an explicit Field apply to this command's tool.
	Availability []*Rule
	// InputRules gate whether one invocation is legal: rules over the flags
	// this command parses. Only leaf commands can declare them. A rule's
	// Field and Targets name flag names. A requires or enabledWhen rule with
	// no Field constrains the whole invocation instead. Its condition must
	// hold even when the caller supplies no flags.
	InputRules []*Rule
	// Output declares how the command's stdout is decoded and which exit
	// codes mean success. Only leaf commands can declare it.
	Output Output
	// Handle runs the command and returns the result the adapter writes.
	Handle HandleFunc
	// Raw writes directly to stdout.
	Raw RawFunc
	// Stream writes incrementally; Handle, Raw, and Stream are mutually exclusive.
	Stream StreamFunc
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
	// exits with the signal's shell code (130/143). Stream output already
	// written remains on stdout.
	HandlerOwnedSignals bool
	// VersionTemplate renders the --version output; the default is
	// "<name> {{.Version}}\n".
	VersionTemplate string
	// RootShort and RootLong are the bare help's short and long text.
	RootShort   string
	RootLong    string
	Commands    []*Command
	CommonFlags []Flag
	// AvailabilityConditions declares the inputs consumers supply when they
	// evaluate the app's availability rules. Rendered into the generated
	// manifest's availability block; rules referencing an undeclared
	// condition fail validation at startup.
	AvailabilityConditions []Condition
}

// UsageErrorf builds a UsageError with a formatted message.
func UsageErrorf(format string, args ...any) *UsageError {
	return &UsageError{Message: fmt.Sprintf(format, args...)}
}

// Validate checks the whole declaration the way Run and Tree do: names,
// handler exclusivity, leaf-only shapes, availability rules, outputs, and
// flags. The render paths (the schema document and the registry manifest)
// call it so they reject exactly what the tree rejects.
func Validate(app *App) error {
	return validate(app)
}

// validate is Validate's implementation: every command and subcommand has a
// name and a short, the flag lists the tree builder registers are
// well-formed and free of repeats, and handlers are exclusive. Called by
// buildTree so drift fails at startup on every entry point.
func validate(app *App) error {
	checks, err := validateAvailability(app)
	if err != nil {
		return err
	}
	commonNames := make(map[string]bool, len(app.CommonFlags))
	for _, f := range app.CommonFlags {
		commonNames[f.Name] = true
	}
	// The root registers the common flags persistently, so they are the
	// surface every command inherits.
	inherited, err := validateFlags("common", nil, app.CommonFlags, nil)
	if err != nil {
		return err
	}
	for _, cmd := range app.Commands {
		if err := validateCommand(cmd.Name, cmd, inherited, app.CommonFlags, commonNames, checks); err != nil {
			return err
		}
	}
	for _, ref := range checks.fieldRefs {
		if !checks.toolNames[ref.field] {
			return UsageErrorf(
				"command %q availability rule %d targets unknown tool %q",
				ref.path, ref.index, ref.field,
			)
		}
	}
	return nil
}

// surfaceFlag is one flag on a command's parse surface with the validation
// label of the declaration it came from: `command "wf"` for an ancestor's
// persistent flag, "common" for the app's common flags.
type surfaceFlag struct {
	Flag
	declaredBy string
}

// validateCommand checks one declared command and descends into its
// subcommands, carrying the flag surface they inherit: the common flags and
// every ancestor's persistent flags. The availability checks ride the same
// walk: rules declared on this command validate against the shared condition
// set.
func validateCommand(path string, c *Command, inherited []surfaceFlag, common []Flag, commonNames map[string]bool, checks *availabilityChecks) error {
	if c.Name == "" {
		return UsageErrorf("command %q has no name", path)
	}
	if c.Short == "" {
		return UsageErrorf("command %q has no help short", path)
	}
	count := 0
	if c.Handle != nil {
		count++
	}
	if c.Raw != nil {
		count++
	}
	if c.Stream != nil {
		count++
	}
	if len(c.Subcommands) == 0 && count != 1 {
		// Only leaf commands run: a parent with subcommands fails with a
		// caller error when invoked bare.
		return UsageErrorf("command %q must declare exactly one of Handle, Raw, or Stream", path)
	}
	if err := ValidateLeafOnly(path, c); err != nil {
		return err
	}
	if rules := checks.rulesByCmd[c]; len(rules) > 0 {
		if err := validateAvailabilityRules(path, rules, checks); err != nil {
			return err
		}
	}
	if err := c.ValidateOutput(path); err != nil {
		return err
	}
	// The builder registers a command's persistent flags only when the
	// command owns subcommands, and its plain flags only on a leaf; the
	// other list never reaches a flag set.
	registered := c.PersistentFlags
	if len(c.Subcommands) == 0 {
		registered = c.Flags
	}
	surface, err := validateFlags(fmt.Sprintf("command %q", path), inherited, registered, commonNames)
	if err != nil {
		return err
	}
	// Input rules name flags on the surface the command parses, so they
	// validate against that surface rather than the rule set's own list.
	if rules := checks.inputByCmd[c]; len(rules) > 0 {
		if err := validateInputRules(path, rules, exposableSurface(common, registered)); err != nil {
			return err
		}
	}
	for _, sub := range c.Subcommands {
		if sub.Name == "" {
			return UsageErrorf("command %q has a subcommand with no name", path)
		}
		if err := validateCommand(path+" "+sub.Name, sub, surface, common, commonNames, checks); err != nil {
			return err
		}
	}
	return nil
}

// declaresHelp reports whether a command's surface declares a flag named
// "help": a merged flag set that has one keeps cobra from claiming the "h"
// shorthand for its own help flag.
func declaresHelp(inherited []surfaceFlag, flags []Flag) bool {
	for _, f := range inherited {
		if f.Name == "help" {
			return true
		}
	}
	for _, f := range flags {
		if f.Name == "help" {
			return true
		}
	}
	return false
}

// ValidateLeafOnly rejects a parent that declares a member only a leaf may
// carry: rules, positionals, or no_interspersed. Every entry point — the
// tree, the document render, and the manifest — funnels through the tree's
// validate, which owns the leaf-only rejection and calls this helper beside
// ValidateOutput.
func ValidateLeafOnly(path string, cmd *Command) error {
	if len(cmd.Subcommands) == 0 {
		return nil
	}
	if len(cmd.Availability) > 0 || len(cmd.InputRules) > 0 {
		return UsageErrorf("command %q declares rules but owns subcommands; only leaf commands run as tools", path)
	}
	if len(cmd.Positionals) > 0 || cmd.NoInterspersed {
		return UsageErrorf("command %q owns subcommands and declares positionals or no_interspersed", path)
	}
	return nil
}

// ValidateOutput checks one command's output declaration. Only a leaf writes
// output of its own, so a command with subcommands cannot declare one. The
// optional members widen a mode rather than standing alone: a pointer or a
// success-code list without a mode leaves the consumer unable to decode the
// command, and a mode without a success-code list leaves it guessing which
// exits decode.
//
// Run, Parse, and Tree reach it through validate, and so do the render
// paths: the registry manifest and the schema document call the tree's
// validate before rendering, so they reject what the tree rejects.
func (c *Command) ValidateOutput(path string) error {
	o := c.Output
	declared := o.Mode != "" || o.JSONPointer != "" || len(o.SuccessExitCodes) > 0
	if len(c.Subcommands) > 0 {
		if declared {
			return UsageErrorf("command %q declares output, but only a leaf command can", path)
		}
		return nil
	}
	if o.Mode == "" {
		if o.JSONPointer != "" || len(o.SuccessExitCodes) > 0 {
			return UsageErrorf("command %q declares output details without a mode", path)
		}
		return nil
	}
	switch o.Mode {
	case OutputJSON, OutputRaw, OutputNone:
	default:
		return UsageErrorf("command %q output mode %q, want %q, %q, or %q", path, o.Mode, OutputJSON, OutputRaw, OutputNone)
	}
	if o.JSONPointer != "" {
		if o.Mode != OutputJSON {
			return UsageErrorf("command %q declares a json pointer with output mode %q", path, o.Mode)
		}
		if !strings.HasPrefix(o.JSONPointer, "/") {
			return UsageErrorf("command %q json pointer %q, want a path starting with /", path, o.JSONPointer)
		}
	}
	seen := make(map[int]bool, len(o.SuccessExitCodes))
	for _, code := range o.SuccessExitCodes {
		if code < 0 {
			return UsageErrorf("command %q declares success exit code %d, want a non-negative code", path, code)
		}
		if code > 255 {
			return UsageErrorf("command %q declares success exit code %d, want a code in [0, 255]", path, code)
		}
		if seen[code] {
			return UsageErrorf("command %q declares success exit code %d twice", path, code)
		}
		seen[code] = true
	}
	if len(o.SuccessExitCodes) == 0 {
		return UsageErrorf("command %q declares output mode %q without success exit codes", path, o.Mode)
	}
	return nil
}

// validateFlags checks one flag list the tree builder registers: named, known
// kind, a default that matches its kind, a usage string, and a one-letter
// shorthand cobra can register without claiming the help flag's. It returns
// the surface its command parses: the inherited flags the list does not
// shadow, then the list's own, in registration order.
//
// pflag panics at registration on a repeated name or a shared shorthand, so
// both are caller errors here. A flag named "help" anywhere in the surface
// keeps cobra from claiming the "h" shorthand for its own help flag. A common
// flag is declared once on the App, so a command that repeats one is a caller
// error too, while a command may re-declare an ancestor's persistent flag of
// the same kind and cobra then parses the command's own.
func validateFlags(where string, inherited []surfaceFlag, flags []Flag, commonNames map[string]bool) ([]surfaceFlag, error) {
	helpDeclared := declaresHelp(inherited, flags)
	names := make(map[string]bool, len(flags))
	for _, f := range flags {
		switch f.Kind {
		case String, Bool, Repeat, Int, Duration, DurationMS:
		default:
			return nil, UsageErrorf("%s flag %q has unknown kind %d", where, f.Name, f.Kind)
		}
		if err := validateFlagDefault(where, f); err != nil {
			return nil, err
		}
		if f.Usage == "" {
			return nil, UsageErrorf("%s flag %q has no usage string", where, f.Name)
		}
		if f.Name == "" {
			return nil, UsageErrorf("%s has an unnamed flag", where)
		}
		if names[f.Name] {
			return nil, UsageErrorf("%s declares flag %q twice", where, f.Name)
		}
		if commonNames[f.Name] {
			return nil, UsageErrorf("%s flag %q repeats a common flag name", where, f.Name)
		}
		if f.Short != "" && len(f.Short) != 1 {
			return nil, UsageErrorf("%s flag %q uses shorthand %q, want one letter", where, f.Name, f.Short)
		}
		// Cobra's default help flag claims the "h" shorthand on every
		// command whose surface has no flag named "help", during execute; a
		// declaration using it would panic there.
		if f.Short == "h" && !helpDeclared {
			return nil, UsageErrorf("%s flag %q uses shorthand %q, reserved for the help flag", where, f.Name, f.Short)
		}
		// The shadow takes the name over from the inherited flag, so both
		// must agree on the kind: pflag keeps only the shadow's value, while
		// the invocation reads every inherited flag with its own kind.
		for _, prev := range inherited {
			if prev.Name == f.Name && prev.Kind != f.Kind {
				return nil, UsageErrorf("%s flag %q has kind %s, but the same-named flag from %s has kind %s",
					where, f.Name, f.Kind.KindSchema(), prev.declaredBy, prev.Kind.KindSchema())
			}
		}
		names[f.Name] = true
	}
	surface := make([]surfaceFlag, 0, len(inherited)+len(flags))
	for _, f := range inherited {
		// A flag this command declares takes the name, and cobra drops the
		// inherited one along with its shorthand.
		if !names[f.Name] {
			surface = append(surface, f)
		}
	}
	for _, f := range flags {
		if f.Short != "" {
			for _, prev := range surface {
				if prev.Short == f.Short {
					return nil, UsageErrorf("%s flags %q and %q share the %q shorthand", where, prev.Name, f.Name, f.Short)
				}
			}
		}
		surface = append(surface, surfaceFlag{Flag: f, declaredBy: where})
	}
	return surface, nil
}

// validateFlagDefault checks one flag's declared default against its kind.
// The published document renders the default the parser applies, so a
// default the parser would silently ignore is a divergence between the
// contract and the runtime: it fails here, at the declaration site.
func validateFlagDefault(where string, f Flag) error {
	switch f.Kind {
	case Duration:
		if f.Default != nil {
			if _, ok := f.Default.(time.Duration); !ok {
				return UsageErrorf("%s flag %q has a %T default; a duration default is a time.Duration", where, f.Name, f.Default)
			}
		}
	case DurationMS:
		switch v := f.Default.(type) {
		case nil:
		case int:
			if int64(v) > maxDurationMS || int64(v) < -maxDurationMS {
				return UsageErrorf("%s flag %q has an out-of-range default: the largest duration_ms value is %d milliseconds", where, f.Name, maxDurationMS)
			}
		case int64:
			if v > maxDurationMS || v < -maxDurationMS {
				return UsageErrorf("%s flag %q has an out-of-range default: the largest duration_ms value is %d milliseconds", where, f.Name, maxDurationMS)
			}
		case time.Duration:
			if v%time.Millisecond != 0 {
				return UsageErrorf("%s flag %q has a %v default; a duration_ms default is a whole number of milliseconds", where, f.Name, v)
			}
		default:
			return UsageErrorf("%s flag %q has a %T default; a duration_ms default is a whole number of milliseconds", where, f.Name, f.Default)
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
