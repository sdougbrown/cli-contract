package clicontract

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Run executes one CLI invocation: it builds the tree from the declarations,
// parses argv, runs the handler through the adapter, writes exactly one
// result to stdout, and returns the process exit code. Diagnostics go to
// stderr. Invalid declarations panic at startup: a command without help
// text would otherwise render blank instead of failing here.
func Run(app *App, adapter Adapter, argv []string, stdout, stderr io.Writer) int {
	command := ""
	if effective := effectiveArgv(argv); len(effective) > 0 {
		command = effective[0]
	}

	exec := &execution{app: app, adapter: adapter}

	ctx := context.Background()
	sigCh := make(chan os.Signal, 1)
	if app.HandlerOwnedSignals {
		// The handler installs its own signal wiring and picks the exit
		// code; the library stays out of the way.
	} else {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigCh)
	}

	if err := app.execute(exec, true, argv, stdout, stderr, ctx); err != nil {
		// Help never reaches here (cobra writes plain help and returns
		// nil); every error that does is a caller error with the usage
		// envelope and exit 2.
		err = asUsageError(err)
		adapter.Write(stdout, adapter.ErrorResult(command, err))
		if exec.ran || exec.rawRan {
			// A dispatched command owns the failure — a Validate rejection
			// or a raw-output command's error — so the handler-diagnostic
			// hook formats its line.
			writeDiagnostic(adapter, stderr, exec.inv, err)
		} else {
			// Dispatch-level errors are self-prefixed: the messages are
			// built under the app's error prefix at the validation sites.
			writeDispatchDiagnostic(adapter, stderr, exec.diagCommand(command), err)
		}
		return adapter.ExitCode(Invocation{Command: command}, nil, err)
	}
	// Help paths (bare --help, the help command, <command> --help) print
	// plain text and exit 0 without a result; ran is set only when a
	// command dispatched.
	if !exec.ran {
		return ExitOK
	}
	result := exec.result

	// A terminating signal exits with its shell code and no stdout output.
	if !app.HandlerOwnedSignals {
		select {
		case sig := <-sigCh:
			return SignalExitCode(sig)
		default:
		}
	}

	// Test-only crash point: after the result is computed, before the
	// envelope is written. No-op without the TestSeams extension.
	if seams, ok := adapter.(TestSeams); ok {
		seams.BeforeWrite()
	}
	if !app.HandlerOwnedSignals {
		if seams, ok := adapter.(TestSeams); ok {
			if sig, ok := seams.PendingSignal(); ok {
				select {
				case sigCh <- sig:
				default:
				}
			}
		}
	}

	// The result is the machine-readable record; this line keeps the
	// stderr diagnostic promise for handler failures.
	if exec.handlerErr != nil {
		writeDiagnostic(adapter, stderr, exec.inv, exec.handlerErr)
	}

	if err := adapter.Write(stdout, result); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return ExitOperational
	}
	if after, ok := adapter.(AfterWrite); ok {
		after.AfterWrite(exec.inv, result, stderr)
	}
	// A signal that arrived during the write still wins on exit code, even
	// though the envelope may already be written.
	if !app.HandlerOwnedSignals {
		select {
		case sig := <-sigCh:
			return SignalExitCode(sig)
		default:
		}
	}
	return adapter.ExitCode(exec.inv, result, exec.handlerErr)
}

// writeDispatchDiagnostic writes one dispatch-failure diagnostic to stderr.
// The default is the error's message; the DispatchDiagnostics extension
// formats it (or silences it with an empty string).
func writeDispatchDiagnostic(adapter Adapter, stderr io.Writer, command string, err error) {
	line := err.Error()
	if dd, ok := adapter.(DispatchDiagnostics); ok {
		line = dd.DispatchDiagnostic(command, err)
	}
	if line != "" {
		fmt.Fprintln(stderr, line)
	}
}

// writeDiagnostic writes one handler-failure diagnostic to stderr. The
// default is the error's message; the HandlerDiagnostics extension formats
// it (or silences it with an empty string).
func writeDiagnostic(adapter Adapter, stderr io.Writer, inv Invocation, err error) {
	line := err.Error()
	if hd, ok := adapter.(HandlerDiagnostics); ok {
		line = hd.HandlerDiagnostic(inv, err)
	}
	if line != "" {
		fmt.Fprintln(stderr, line)
	}
}

// execute builds the tree and runs the parse-and-dispatch passes over argv,
// handing cobra the argv untouched (nil argv falls back to os.Args[1:]
// inside cobra). A pass that fails with an error a single-dash long flag
// can produce runs again with that failing command's spellings rewritten to
// their double-dash form; each rewrite can expose a deeper command, so the
// passes repeat while a rewrite changes something. Every pass that retries
// rewrites at least one token and a rewrite only adds a dash to a token, so
// the passes are bounded by argv's token count. A pass that rewrites
// nothing, or a last pass that still fails before dispatch, reports the
// first pass's error unchanged.
//
// A pass that reached a RunE owns its outcome: its result and its error are
// what the caller gets, so a retry that dispatched is never masked by the
// parse error that triggered it.
func (a *App) execute(exec *execution, runHandlers bool, argv []string, stdout, stderr io.Writer, ctx context.Context) error {
	var firstErr error
	var firstCmd *cobra.Command
	limit := len(effectiveArgv(argv)) + 1
	for attempt := 0; attempt < limit; attempt++ {
		// Each pass records its own flag failure: the command cobra
		// resolved is what the next rewrite is scoped to.
		exec.flagErr, exec.flagErrCmd = nil, nil
		root := readyTree(a, exec, runHandlers, argv, stdout, stderr)
		// ExecuteContext is ExecuteContextC with the command discarded; the
		// command is what the dispatch-diagnostic extensions need.
		cmd, err := root.ExecuteContextC(ctx)
		if err == nil {
			return nil
		}
		if attempt == 0 {
			firstErr, firstCmd = err, cmd
		}
		if exec.ran || exec.rawRan {
			return err
		}
		retryArgv := a.retryArgv(exec, root, argv)
		if retryArgv == nil {
			// The reported error is the first pass's: its own resolved
			// command must pair with it, not the last pass's.
			exec.failCmd = firstCmd
			return firstErr
		}
		// The failure preceded every RunE, so no handler has run: rebuild
		// for clean flag state and parse the rewritten argv again.
		argv = retryArgv
	}
	exec.failCmd = firstCmd
	return firstErr
}

// readyTree builds one execution's cobra tree bound to its writers and
// argv.
func readyTree(a *App, exec *execution, runHandlers bool, argv []string, stdout, stderr io.Writer) *cobra.Command {
	root := a.buildTree(exec, runHandlers)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(argv)
	return root
}

// HandlerDiagnostics is an optional Adapter extension that formats the
// stderr diagnostic for handler-owned failures: a dispatched command's
// error, a Validate rejection, or a raw-output command's failure. The
// default prints the error's message; an empty string prints nothing.
type HandlerDiagnostics interface {
	HandlerDiagnostic(inv Invocation, err error) string
}

// DispatchDiagnostics is an optional Adapter extension that formats the
// stderr diagnostic for failures before dispatch (unknown command, bad
// flag, bad positional). Once a command dispatches, its failure goes to
// HandlerDiagnostics instead. The default prints the error's message; an
// empty string prints nothing.
//
// The command argument is the path the failure resolved to: names below
// the root joined with spaces ("store reset"), or the app name when the
// failure resolved to the root. It is the same path the diagnostic
// message is prefixed with. It is a path rather than an Invocation
// because no Invocation exists before dispatch.
type DispatchDiagnostics interface {
	DispatchDiagnostic(command string, err error) string
}

// diagCommand resolves the command name the dispatch-diagnostic extensions
// see: the path of the command cobra resolved for the reported error, or
// the argv-derived name when no tree ran to resolve one.
func (exec *execution) diagCommand(fallback string) string {
	if exec.failCmd == nil {
		return fallback
	}
	return exec.app.pathOf(exec.failCmd)
}

// Parse parses argv (without the program name) without running anything:
// it validates the declaration like Run, executes the tree in parse-only
// mode, so flags and positionals are validated by the same Args functions
// dispatch uses, and returns the captured Invocation. Commands that
// produce no Invocation (help paths, raw-output commands) return a caller
// error, matching Run's no-envelope behavior for them. Nil argv is the
// process's own arguments, as in Run.
func Parse(app *App, argv []string) (Invocation, error) {
	exec := &execution{app: app, adapter: nil}
	if err := app.execute(exec, false, argv, io.Discard, io.Discard, context.Background()); err != nil {
		return Invocation{}, asUsageError(err)
	}
	if !exec.ran && !exec.rawRan {
		return Invocation{}, UsageErrorf("missing subcommand")
	}
	return exec.inv, nil
}

// Find resolves one command by name, including nested names ("parent sub").
// It returns nil when no declared command matches.
func (a *App) Find(name string) *Command {
	return a.find(name)
}
