package clicontract

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Run executes one CLI invocation: it builds the tree from the declarations,
// parses argv, runs the handler through the adapter, writes exactly one
// result to stdout, and returns the process exit code. Diagnostics go to
// stderr. Invalid declarations panic at startup: a command without help
// text would otherwise render blank instead of failing here.
func Run(app *App, adapter Adapter, argv []string, stdout, stderr io.Writer) int {
	if err := validate(app); err != nil {
		panic(err.Error())
	}
	command := ""
	if len(argv) > 0 {
		command = argv[0]
	}

	exec := &execution{app: app, adapter: adapter}
	root := app.buildTree(exec, true)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(argv)

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

	if err := root.ExecuteContext(ctx); err != nil {
		// Help never reaches here (cobra writes plain help and returns
		// nil); every error that does is a caller error with the usage
		// envelope and exit 2.
		err = asUsageError(err)
		adapter.Write(stdout, adapter.ErrorResult(command, err))
		// Dispatch-level errors are self-prefixed: the messages are built
		// under the app's error prefix at the validation sites.
		fmt.Fprintln(stderr, err.Error())
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

// HandlerDiagnostics is an optional Adapter extension that formats the
// stderr diagnostic for handler failures. The default prints the error's
// message; an empty string prints nothing.
type HandlerDiagnostics interface {
	HandlerDiagnostic(inv Invocation, err error) string
}

// Parse parses argv (without the program name) without running anything:
// it validates the declaration like Run, executes the tree in parse-only
// mode, so flags and positionals are validated by the same Args functions
// dispatch uses, and returns the captured Invocation. Commands that
// produce no Invocation (help paths, raw-output commands) return a caller
// error, matching Run's no-envelope behavior for them.
func Parse(app *App, argv []string) (Invocation, error) {
	if err := validate(app); err != nil {
		panic(err.Error())
	}
	if len(argv) == 0 {
		return Invocation{}, UsageErrorf("missing subcommand")
	}
	exec := &execution{app: app, adapter: nil}
	root := app.buildTree(exec, false)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(argv)
	if err := root.Execute(); err != nil {
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
