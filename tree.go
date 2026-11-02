package clicontract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// exit codes and signal conventions are the library's: help and version
// exit 0, terminating signals exit with their shell codes, and every other
// exit code is the adapter's call.
const (
	// ExitOK reports success, including envelope statuses that are not
	// errors.
	ExitOK = 0
	// ExitOperational is reserved for the adapter's operational failures.
	ExitOperational = 1
)

// SignalExitCode maps terminating signals to shell-style exit codes: SIGINT
// exits 130 and SIGTERM exits 143.
func SignalExitCode(sig os.Signal) int {
	switch sig {
	case os.Interrupt:
		return 130
	case syscall.SIGTERM:
		return 143
	default:
		return ExitOperational
	}
}

// Adapter is the app's result vocabulary. The library routes invocations and
// caller errors; the adapter formats results, writes them, and derives exit
// codes.
type Adapter interface {
	// ErrorResult builds the result written for a dispatch-level (caller)
	// error or a handler failure.
	ErrorResult(command string, err error) any
	// Write emits one result to stdout. It must write exactly one JSON
	// value for envelope-style apps.
	Write(w io.Writer, result any) error
	// ExitCode derives the process exit code after the result is written.
	// handlerErr carries the handler's error (nil on success); dispatch-
	// level failures arrive as a *UsageError with an Invocation whose
	// Command is argv[0].
	ExitCode(inv Invocation, result any, handlerErr error) int
	// Deadline decorates the context before the handler runs. Return the
	// context unchanged and a no-op cancel when the command needs none.
	Deadline(ctx context.Context, inv Invocation) (context.Context, context.CancelFunc)
	// BeforeHandler runs after parsing and before the handler: deprecation
	// notices and other pre-flight diagnostics go here.
	BeforeHandler(inv Invocation, stderr io.Writer)
	// Validate reports caller errors that stop the command before it runs
	// (for example a malformed duration on a flag the handler would
	// otherwise parse leniently). A non-nil error routes through the
	// dispatch error path.
	Validate(inv Invocation) error
}

// TestSeams is an optional Adapter extension for test-only crash and signal
// injection. Production adapters do not implement it.
type TestSeams interface {
	// BeforeWrite is the crash point after the result is computed and
	// before the envelope is written.
	BeforeWrite()
	// PendingSignal returns a signal to inject before the envelope write,
	// exercising the post-write signal re-check.
	PendingSignal() (os.Signal, bool)
}

// AfterWrite is an optional Adapter extension that runs after the envelope
// is written and before the exit code is derived: monitor-facing summaries
// on stderr go here.
type AfterWrite interface {
	AfterWrite(inv Invocation, result any, stderr io.Writer)
}

// execution collects what one command execution produced. The same tree
// serves dispatch (handlers run) and parse-only (Parse captures without
// executing).
type execution struct {
	app        *App
	adapter    Adapter
	inv        Invocation
	result     any
	handlerErr error
	ran        bool
}

// asUsageError guarantees the usage classification for every dispatch-level
// failure: unknown commands, flag errors, and positional validation are all
// caller errors whatever error type cobra raised internally.
func asUsageError(err error) error {
	var ue *UsageError
	if errors.As(err, &ue) {
		return ue
	}
	return &UsageError{Message: err.Error()}
}

// addFlags registers one command's flags on a flag set.
func addFlags(fs *pflag.FlagSet, flags []Flag) {
	for _, f := range flags {
		switch f.Kind {
		case Bool:
			if f.Short != "" {
				fs.BoolP(f.Name, f.Short, false, f.Usage)
			} else {
				fs.Bool(f.Name, false, f.Usage)
			}
		case Repeat:
			fs.StringArray(f.Name, nil, f.Usage)
		case Int:
			fs.Int(f.Name, 0, f.Usage)
		case Duration:
			fs.Duration(f.Name, 0, f.Usage)
		default:
			fs.String(f.Name, "", f.Usage)
		}
	}
}

// noArgs validates that a command took no positionals.
func noArgs(name string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return UsageErrorf("%s: unexpected argument %q", name, args[0])
		}
		return nil
	}
}

// invocationFrom builds the Invocation from one parsed cobra command.
func invocationFrom(cmd *cobra.Command, c *Command, parent *Command, args []string, common []Flag) (Invocation, error) {
	inv := Invocation{
		Sub:       c.Name,
		Args:      args, // validated by positionalArgs
		Flags:     map[string]string{},
		Bools:     map[string]bool{},
		Ints:      map[string]int{},
		Durations: map[string]time.Duration{},
	}
	if parent != nil {
		inv.Command = parent.Name
	} else {
		inv.Command = c.Name
		inv.Sub = ""
	}
	fs := cmd.Flags()
	read := func(f Flag) error {
		switch f.Kind {
		case Bool:
			b, err := fs.GetBool(f.Name)
			if err != nil {
				return err
			}
			inv.Bools[f.Name] = b
		case Repeat:
			arr, err := fs.GetStringArray(f.Name)
			if err != nil {
				return err
			}
			inv.Events = arr
		case Int:
			n, err := fs.GetInt(f.Name)
			if err != nil {
				return err
			}
			inv.Ints[f.Name] = n
		case Duration:
			d, err := fs.GetDuration(f.Name)
			if err != nil {
				return err
			}
			inv.Durations[f.Name] = d
		default:
			s, err := fs.GetString(f.Name)
			if err != nil {
				return err
			}
			inv.Flags[f.Name] = s
		}
		return nil
	}
	for _, f := range common {
		if err := read(f); err != nil {
			return Invocation{}, err
		}
	}
	for _, f := range c.Flags {
		if fs.Lookup(f.Name) == nil {
			continue // nested subcommands may not carry every parent flag
		}
		if err := read(f); err != nil {
			return Invocation{}, err
		}
	}
	if parent != nil {
		for _, f := range parent.PersistentFlags {
			if err := read(f); err != nil {
				return Invocation{}, err
			}
		}
	}
	return inv, nil
}

// dispatchRunE builds the RunE shared by dispatching commands: capture the
// Invocation, and in dispatch mode run the handler and record the result.
func dispatchRunE(exec *execution, runHandlers bool, c *Command, parent *Command, common []Flag) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		inv, err := invocationFrom(cmd, c, parent, args, common)
		if err != nil {
			return err
		}
		exec.inv = inv
		exec.ran = true
		if !runHandlers {
			return nil
		}
		if verr := exec.adapter.Validate(inv); verr != nil {
			return verr
		}
		exec.adapter.BeforeHandler(inv, cmd.ErrOrStderr())
		exec.invocation(cmd.Context(), c, inv, cmd.ErrOrStderr())
		return nil
	}
}

// rawRunE builds the RunE for raw-output commands.
func rawRunE(exec *execution, runHandlers bool, c *Command, common []Flag) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		inv, err := invocationFrom(cmd, c, nil, args, common)
		if err != nil {
			return err
		}
		if runHandlers {
			if err := c.Raw(cmd.Context(), inv, cmd.OutOrStdout()); err != nil {
				return err
			}
		}
		// ran stays false: a raw command writes its own output and exits 0
		// without an envelope.
		return nil
	}
}

// invocation runs one command's handler with the decorated context and
// normalizes handler failures through the adapter's error result.
func (exec *execution) invocation(ctx context.Context, c *Command, inv Invocation, stderr io.Writer) {
	var cancel context.CancelFunc
	ctx, cancel = exec.adapter.Deadline(ctx, inv)
	defer cancel()
	result, err := c.Handle(ctx, inv, stderr)
	if err != nil {
		exec.result = exec.adapter.ErrorResult(inv.Command, err)
		exec.handlerErr = err
		return
	}
	exec.result, exec.handlerErr = result, nil
}

// buildTree builds the cobra command tree from the declared commands. The
// declarations are the single source of truth: every flag, positional rule,
// and handler binding derives from them, and validate() fails startup on
// drift.
func (a *App) buildTree(exec *execution, runHandlers bool) *cobra.Command {
	root := &cobra.Command{
		Use:     a.Name,
		Short:   a.RootShort,
		Long:    a.RootLong,
		Version: a.Version,
		// Dispatch never returns operational failures: everything that
		// escapes the tree is a caller error, so keep cobra silent and map
		// errors to the usage envelope in Run.
		SilenceErrors:      true,
		SilenceUsage:       true,
		DisableSuggestions: true,
		// The generated completion command is not part of the documented
		// surface; the hidden __complete protocol stays available.
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.BareHelp {
				return cmd.Help()
			}
			return UsageErrorf("missing subcommand")
		},
	}
	versionTemplate := a.VersionTemplate
	if versionTemplate == "" {
		versionTemplate = fmt.Sprintf("%s {{.Version}}\n", a.Name)
	}
	root.SetVersionTemplate(versionTemplate)
	// Flag errors and unknown commands are caller errors: code them as
	// usage errors so the envelope reports the usage classification.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return a.errf(a.pathOf(root, cmd), "%v", err)
	})
	// The default help command reports unknown topics on stdout and exits
	// 0, breaching the one-JSON-envelope contract; this replacement routes
	// unknown help topics to the usage envelope instead.
	root.SetHelpCommand(&cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, _, err := root.Find(args)
			if err != nil || (len(args) > 0 && target == root) {
				return UsageErrorf("unknown help topic %q", strings.Join(args, " "))
			}
			target.InitDefaultHelpFlag()
			return target.Help()
		},
	})
	addFlags(root.PersistentFlags(), a.CommonFlags)

	for _, c := range a.Commands {
		if len(c.Subcommands) > 0 {
			root.AddCommand(a.buildSubTree(exec, runHandlers, c))
			continue
		}
		var runE func(*cobra.Command, []string) error
		if c.Raw != nil {
			runE = rawRunE(exec, runHandlers, c, a.CommonFlags)
		} else {
			runE = dispatchRunE(exec, runHandlers, c, nil, a.CommonFlags)
		}
		args := a.positionalArgs(c.Name, c)
		cmd := &cobra.Command{
			Use:   commandUse(c),
			Short: c.Short,
			Long:  c.Long,
			Args:  args,
			RunE:  runE,
		}
		if c.NoInterspersed {
			cmd.Flags().SetInterspersed(false)
		}
		addFlags(cmd.Flags(), c.Flags)
		root.AddCommand(cmd)
	}
	return root
}

// pathOf derives the full command path for diagnostics: the app name at
// the root, parent names joined with spaces below it.
func (a *App) pathOf(root *cobra.Command, c *cobra.Command) string {
	if c == root {
		return a.Name
	}
	name := c.Name()
	for p := c.Parent(); p != nil && p != root; p = p.Parent() {
		name = p.Name() + " " + name
	}
	return name
}

// buildSubTree builds a command that owns nested subcommands.
func (a *App) buildSubTree(exec *execution, runHandlers bool, c *Command) *cobra.Command {
	parent := &cobra.Command{
		Use:   commandUse(c),
		Short: c.Short,
		Long:  c.Long,
		RunE: func(cmd *cobra.Command, args []string) error {
			quoted := make([]string, len(c.Subcommands))
			for i, sub := range c.Subcommands {
				quoted[i] = fmt.Sprintf("%q", sub.Name)
			}
			return UsageErrorf("%s requires a subcommand: %s", c.Name, strings.Join(quoted, " or "))
		},
	}
	addFlags(parent.PersistentFlags(), c.PersistentFlags)
	for _, sub := range c.Subcommands {
		subCmd := &cobra.Command{
			Use:   sub.Name,
			Short: sub.Short,
			Long:  sub.Long,
			Args:  noArgs(c.Name + " " + sub.Name),
			RunE:  dispatchRunE(exec, runHandlers, sub, c, a.CommonFlags),
		}
		addFlags(subCmd.Flags(), sub.Flags)
		parent.AddCommand(subCmd)
	}
	return parent
}

// Tree builds the cobra command tree without an adapter: for tests and
// introspection that walk the registered surface. Dispatching from the
// returned tree is not supported.
func (a *App) Tree() *cobra.Command {
	exec := &execution{app: a}
	return a.buildTree(exec, false)
}
