package clicontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// testAdapter mirrors an envelope-style app: one JSON result on stdout,
// usage errors classified to exit 2, operational to 1, success to 0.
type testAdapter struct {
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	wrote    []any
	deadline func(ctx context.Context, inv Invocation) (context.Context, context.CancelFunc)
	notice   []string
	seams    *testSeams
	after    []string
}

func (a *testAdapter) ErrorResult(command string, err error) any {
	var ue *UsageError
	if errors.As(err, &ue) {
		return map[string]any{"command": command, "status": "error", "error": map[string]any{"code": "usage", "message": ue.Message}}
	}
	return map[string]any{"command": command, "status": "error", "error": map[string]any{"code": "internal", "message": err.Error()}}
}

func (a *testAdapter) Write(w io.Writer, result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	w.Write(append(b, '\n'))
	a.wrote = append(a.wrote, result)
	return nil
}

func (a *testAdapter) ExitCode(inv Invocation, result any, handlerErr error) int {
	if result == nil {
		// Dispatch-level failure: no result was written beyond the error
		// envelope; the classification comes from the error alone.
		var ue *UsageError
		if errors.As(handlerErr, &ue) {
			return 2
		}
		return 1
	}
	env := result.(map[string]any)
	if env["status"] == "error" {
		if handlerErr != nil {
			var ue *UsageError
			if errors.As(handlerErr, &ue) {
				return 2
			}
		}
		return 1
	}
	if inv.Command == "wait" && inv.Bools["exit-on-events"] {
		if env["status"] == "events" {
			return 3
		}
	}
	return 0
}

func (a *testAdapter) Validate(inv Invocation) error { return nil }

func (a *testAdapter) Deadline(ctx context.Context, inv Invocation) (context.Context, context.CancelFunc) {
	if a.deadline != nil {
		return a.deadline(ctx, inv)
	}
	return ctx, func() {}
}

func (a *testAdapter) BeforeHandler(inv Invocation, stderr io.Writer) {
	if len(a.notice) > 0 {
		stderr.Write([]byte(strings.Join(a.notice, "; ") + "\n"))
	}
}

func (a *testAdapter) BeforeWrite() {
	if a.seams != nil {
		a.seams.BeforeWrite()
	}
}

func (a *testAdapter) PendingSignal() (os.Signal, bool) {
	if a.seams != nil {
		return a.seams.PendingSignal()
	}
	return nil, false
}

// Write signature conformity: Adapter.Write takes io.Writer.
var _ Adapter = (*testAdapter)(nil)

type testSeams struct {
	crash  bool
	signal os.Signal
}

func (s *testSeams) BeforeWrite() {
	if s.crash {
		panic("crash")
	}
}

func (s *testSeams) PendingSignal() (os.Signal, bool) {
	if s.signal != nil {
		return s.signal, true
	}
	return nil, false
}

// testApp declares a small surface that mirrors an envelope-style CLI.
func testApp() *App {
	return &App{
		Name:      "testcli",
		Version:   "vtest",
		RootShort: "testcli observes things",
		RootLong:  "testcli observes things and delivers events.",
		Commands: []*Command{
			{
				Name:        "reconcile",
				Short:       "collect feedback and establish the baseline",
				Positionals: []Positional{{Name: "URL", Required: true}},
				Flags: []Flag{
					{Name: "head", Kind: String, Usage: "expected head SHA"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{"command": "reconcile", "status": "updated", "url": inv.Args[0], "head": inv.Flags["head"]}, nil
				},
			},
			{
				Name:        "wait",
				Short:       "block until new events arrive",
				Positionals: []Positional{{Name: "URL", Required: true}},
				Flags: []Flag{
					{Name: "consumer", Kind: String, Usage: "name identifying this reader"},
					{Name: "timeout", Kind: String, Usage: "maximum wait duration", Default: "30m"},
					{Name: "exit-on-events", Kind: Bool, Usage: "exit 3 on events"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					if inv.Flags["consumer"] == "" {
						return nil, UsageErrorf("wait requires --consumer")
					}
					if inv.Flags["consumer"] == "fail" {
						return nil, errors.New("boom")
					}
					return map[string]any{"command": "wait", "status": "events"}, nil
				},
			},
			{
				Name:        "ack",
				Short:       "acknowledge delivered event IDs",
				Positionals: []Positional{{Name: "URL", Required: true}},
				Flags: []Flag{
					{Name: "event", Kind: Repeat, Usage: "event ID to acknowledge"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{"command": "ack", "status": "ok", "events": inv.Events}, nil
				},
			},
			{
				Name:  "store",
				Short: "disposable store utilities",
				Subcommands: []*Command{
					{
						Name:  "path",
						Short: "print the resolved store directory",
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{"command": "store", "status": "ok"}, nil
						},
					},
					{
						Name:  "reset",
						Short: "delete the store",
						Flags: []Flag{{Name: "target", Kind: String, Usage: "scope the reset to one target URL"}},
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{"command": "store", "status": "ok", "sub": inv.Sub}, nil
						},
					},
				},
			},
			{
				Name:  "schema",
				Short: "print the interface contract as JSON",
				Raw: func(ctx context.Context, inv Invocation, stdout io.Writer) error {
					stdout.Write([]byte(`{"schema":"v1"}` + "\n"))
					return nil
				},
			},
		},
		CommonFlags: []Flag{
			{Name: "json", Kind: Bool, Usage: "no-op; output is always JSON"},
			{Name: "state-dir", Kind: String, Usage: "override the state directory"},
		},
	}
}

func runApp(t *testing.T, app *App, adapter Adapter, argv ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(app, adapter, argv, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunUsageErrors(t *testing.T) {
	adapter := &testAdapter{}
	tests := []struct {
		name string
		argv []string
	}{
		{"missing subcommand", nil},
		{"unknown subcommand", []string{"bogus"}},
		{"reconcile missing url", []string{"reconcile"}},
		{"two positionals", []string{"reconcile", "u1", "u2"}},
		{"unknown flag", []string{"reconcile", "--bogus", "u1"}},
		{"unknown help topic", []string{"help", "bogus"}},
		{"schema stray positional", []string{"schema", "extra"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runApp(t, testApp(), adapter, tc.argv...)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
				t.Fatalf("stdout is not one JSON value: %v (%q)", err, stdout)
			}
			errObj := envelope["error"].(map[string]any)
			if errObj["code"] != "usage" {
				t.Errorf("error code = %v, want usage", errObj["code"])
			}
		})
	}
}

// diagAdapter extends testAdapter with both diagnostic extensions; each
// field is the line the corresponding extension returns.
type diagAdapter struct {
	*testAdapter
	dispatchLine   string
	handlerLine    string
	validateErr    error
	gotDispatchCmd string
	gotDispatchErr error
	gotHandlerInv  Invocation
	gotHandlerErr  error
}

func (a *diagAdapter) DispatchDiagnostic(command string, err error) string {
	a.gotDispatchCmd, a.gotDispatchErr = command, err
	return a.dispatchLine
}

func (a *diagAdapter) HandlerDiagnostic(inv Invocation, err error) string {
	a.gotHandlerInv, a.gotHandlerErr = inv, err
	return a.handlerLine
}

// validateErr fails the next Validate call, keeping the rest of the
// embedded adapter's behavior.
func (a *diagAdapter) Validate(inv Invocation) error {
	if a.validateErr != nil {
		return a.validateErr
	}
	return a.testAdapter.Validate(inv)
}

func TestRunDispatchDiagnostics(t *testing.T) {
	t.Run("default prints the raw message", func(t *testing.T) {
		_, _, stderr := runApp(t, testApp(), &testAdapter{}, "reconcile", "--bogus", "u1")
		if stderr != "reconcile: unknown flag: --bogus\n" {
			t.Errorf("stderr = %q, want the raw unprefixed message", stderr)
		}
	})

	t.Run("extension replaces the line", func(t *testing.T) {
		adapter := &diagAdapter{testAdapter: &testAdapter{}, dispatchLine: "Run `wt reconcile --help` for usage."}
		code, stdout, stderr := runApp(t, testApp(), adapter, "reconcile", "--bogus", "u1")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
		}
		if stderr != "Run `wt reconcile --help` for usage.\n" {
			t.Errorf("stderr = %q, want the extension's line", stderr)
		}
		if adapter.gotDispatchCmd != "reconcile" {
			t.Errorf("command = %q, want reconcile", adapter.gotDispatchCmd)
		}
		var ue *UsageError
		if !errors.As(adapter.gotDispatchErr, &ue) {
			t.Errorf("err = %v, want a *UsageError", adapter.gotDispatchErr)
		}
		// The stdout envelope keeps the usage classification and the raw
		// message; only the stderr line changes.
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatalf("stdout is not one JSON value: %v (%q)", err, stdout)
		}
		errObj := envelope["error"].(map[string]any)
		if errObj["code"] != "usage" || errObj["message"] == "Run `wt reconcile --help` for usage." {
			t.Errorf("envelope error = %v, want usage code with the raw message", errObj)
		}
	})

	t.Run("empty string silences", func(t *testing.T) {
		adapter := &diagAdapter{testAdapter: &testAdapter{}}
		code, _, stderr := runApp(t, testApp(), adapter, "reconcile", "--bogus", "u1")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want silence", stderr)
		}
	})

	t.Run("nested failure gets the full path", func(t *testing.T) {
		adapter := &diagAdapter{testAdapter: &testAdapter{}, dispatchLine: "hint"}
		code, _, stderr := runApp(t, testApp(), adapter, "store", "reset", "--bogus")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
		}
		if adapter.gotDispatchCmd != "store reset" {
			t.Errorf("command = %q, want the resolved path store reset", adapter.gotDispatchCmd)
		}
		if stderr != "hint\n" {
			t.Errorf("stderr = %q, want the extension's line", stderr)
		}
	})

	t.Run("unresolved command gets the app name", func(t *testing.T) {
		adapter := &diagAdapter{testAdapter: &testAdapter{}, dispatchLine: "hint"}
		code, _, stderr := runApp(t, testApp(), adapter, "bogus")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
		}
		if adapter.gotDispatchCmd != "testcli" {
			t.Errorf("command = %q, want the app name testcli", adapter.gotDispatchCmd)
		}
		if stderr != "hint\n" {
			t.Errorf("stderr = %q, want the extension's line", stderr)
		}
	})

	t.Run("missing subcommand gets the app name", func(t *testing.T) {
		adapter := &diagAdapter{testAdapter: &testAdapter{}, dispatchLine: "hint"}
		code, _, stderr := runApp(t, testApp(), adapter)
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
		}
		if adapter.gotDispatchCmd != "testcli" {
			t.Errorf("command = %q, want the app name testcli", adapter.gotDispatchCmd)
		}
	})

	t.Run("single-dash retry reports the first pass's error", func(t *testing.T) {
		adapter := &diagAdapter{testAdapter: &testAdapter{}, dispatchLine: "hint"}
		// Two passes: -head rewrites to --head, the retry then fails on the
		// missing value, and the extension must see the first pass's error
		// and the original command, not anything from the rewrite.
		code, _, stderr := runApp(t, testApp(), adapter, "reconcile", "u1", "-head")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
		}
		if stderr != "hint\n" {
			t.Errorf("stderr = %q, want the extension's line", stderr)
		}
		if adapter.gotDispatchCmd != "reconcile" {
			t.Errorf("command = %q, want reconcile", adapter.gotDispatchCmd)
		}
		var ue *UsageError
		if !errors.As(adapter.gotDispatchErr, &ue) || !strings.Contains(ue.Message, "unknown shorthand") {
			t.Errorf("err = %v, want the first pass's unknown-shorthand *UsageError", adapter.gotDispatchErr)
		}
	})

	t.Run("masked first error keeps its own command", func(t *testing.T) {
		adapter := &diagAdapter{testAdapter: &testAdapter{}}
		// Two passes: -state-dir fails at the root (unknown command "x"
		// blocks resolution), the rewrite consumes x as --state-dir's
		// value, and pass 1 resolves the deeper store reset and fails
		// there. The reported error is pass 0's, so the extension must see
		// the root's app name, not store reset.
		code, _, _ := runApp(t, testApp(), adapter, "-state-dir", "x", "-json", "store", "reset", "extra")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if adapter.gotDispatchCmd != "testcli" {
			t.Errorf("command = %q, want the app name paired with the first pass's error", adapter.gotDispatchCmd)
		}
	})

	t.Run("raw-command failures keep HandlerDiagnostics", func(t *testing.T) {
		app := &App{Name: "testcli", Commands: []*Command{{
			Name:  "dump",
			Short: "dump the state",
			Raw: func(ctx context.Context, inv Invocation, stdout io.Writer) error {
				return errors.New("dump failed")
			},
		}}}
		adapter := &diagAdapter{testAdapter: &testAdapter{}, handlerLine: "handler line"}
		code, _, stderr := runApp(t, app, adapter, "dump")
		// The usage classification of a raw failure is the pre-existing
		// behavior; the change under test is only the stderr hook.
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
		}
		if stderr != "handler line\n" {
			t.Errorf("stderr = %q, want the handler diagnostic", stderr)
		}
		if adapter.gotDispatchCmd != "" {
			t.Errorf("DispatchDiagnostic ran for a raw-command failure (%q)", adapter.gotDispatchCmd)
		}
	})

	t.Run("Validate rejections keep HandlerDiagnostics and the raised error", func(t *testing.T) {
		validateErr := errors.New("probe rejected")
		adapter := &diagAdapter{testAdapter: &testAdapter{}, handlerLine: "handler line", validateErr: validateErr}
		code, stdout, stderr := runApp(t, testApp(), adapter, "reconcile", "u")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
		}
		if stderr != "handler line\n" {
			t.Errorf("stderr = %q, want the handler diagnostic", stderr)
		}
		if adapter.gotDispatchCmd != "" {
			t.Errorf("DispatchDiagnostic ran for a Validate rejection (%q)", adapter.gotDispatchCmd)
		}
		if adapter.gotHandlerInv.Command != "reconcile" {
			t.Errorf("HandlerDiagnostic command = %q, want reconcile", adapter.gotHandlerInv.Command)
		}
		// The hook sees the error the adapter raised, not the
		// usage-classified copy: identity, not just message equality.
		if adapter.gotHandlerErr != validateErr {
			t.Errorf("HandlerDiagnostic err = %v, want the raised error itself", adapter.gotHandlerErr)
		}
		// The stdout envelope keeps the usage classification.
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatalf("stdout is not one JSON value: %v (%q)", err, stdout)
		}
		if envelope["error"].(map[string]any)["code"] != "usage" {
			t.Errorf("envelope error = %v, want usage code", envelope["error"])
		}
	})

	t.Run("handler failures keep HandlerDiagnostics", func(t *testing.T) {
		adapter := &diagAdapter{testAdapter: &testAdapter{}, dispatchLine: "dispatch", handlerLine: "handler line"}
		code, _, stderr := runApp(t, testApp(), adapter, "wait", "--consumer", "fail", "u")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (stderr: %s)", code, stderr)
		}
		if stderr != "handler line\n" {
			t.Errorf("stderr = %q, want the handler diagnostic", stderr)
		}
		if adapter.gotDispatchCmd != "" {
			t.Errorf("DispatchDiagnostic ran for a dispatched command (%q)", adapter.gotDispatchCmd)
		}
		// Pin the arguments the handler extension actually receives.
		if adapter.gotHandlerInv.Command != "wait" {
			t.Errorf("HandlerDiagnostic command = %q, want wait", adapter.gotHandlerInv.Command)
		}
		if adapter.gotHandlerErr == nil || adapter.gotHandlerErr.Error() != "boom" {
			t.Errorf("HandlerDiagnostic err = %v, want the handler's boom error", adapter.gotHandlerErr)
		}
	})
}

// TestErrorResultCommandIsTheDeclaredPath pins the ErrorResult contract: a
// failure reports the full declared command path it resolved to, the key a
// nested command's output declaration is stored under. A failure that matched
// no declared command reports the top-level argv token the caller typed.
func TestErrorResultCommandIsTheDeclaredPath(t *testing.T) {
	envelopeCommand := func(t *testing.T, stdout string) any {
		t.Helper()
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatalf("stdout is not one JSON value: %v (%q)", err, stdout)
		}
		return envelope["command"]
	}

	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"top-level dispatch failure", []string{"reconcile", "--bogus", "u"}, "reconcile"},
		{"nested dispatch failure", []string{"store", "reset", "--bogus"}, "store reset"},
		{"bare parent failure", []string{"store"}, "store"},
		{"unknown command keeps the typed token", []string{"bogus"}, "bogus"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runApp(t, testApp(), &testAdapter{}, tc.argv...)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
			}
			if got := envelopeCommand(t, stdout); got != tc.want {
				t.Errorf("command = %v, want %q", got, tc.want)
			}
		})
	}

	t.Run("nested handler failure", func(t *testing.T) {
		app := &App{
			Name: "ncli",
			Commands: []*Command{{
				Name:  "wf",
				Short: "workflows",
				Subcommands: []*Command{{
					Name:  "run",
					Short: "run one workflow",
					Handle: func(context.Context, Invocation, io.Writer) (any, error) {
						return nil, UsageErrorf("no such workflow")
					},
				}},
			}},
		}
		code, stdout, stderr := runApp(t, app, &testAdapter{}, "wf", "run")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, stderr)
		}
		if got := envelopeCommand(t, stdout); got != "wf run" {
			t.Errorf("command = %v, want the declared path wf run", got)
		}
	})
}

func TestRunEnvelopePaths(t *testing.T) {
	t.Run("success writes the handler result", func(t *testing.T) {
		adapter := &testAdapter{}
		code, stdout, _ := runApp(t, testApp(), adapter, "reconcile", "https://example.test/pull/1", "--head", "abc")
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["status"] != "updated" || envelope["url"] != "https://example.test/pull/1" || envelope["head"] != "abc" {
			t.Errorf("envelope = %v", envelope)
		}
	})

	t.Run("flags-first ordering", func(t *testing.T) {
		adapter := &testAdapter{}
		code, stdout, _ := runApp(t, testApp(), adapter, "reconcile", "--head", "abc", "https://example.test/pull/1")
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["head"] != "abc" {
			t.Errorf("envelope = %v", envelope)
		}
	})

	t.Run("repeated event flags collect", func(t *testing.T) {
		adapter := &testAdapter{}
		code, stdout, _ := runApp(t, testApp(), adapter, "ack", "--event=e1", "--event=e2", "u")
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatal(err)
		}
		events := envelope["events"].([]any)
		if len(events) != 2 || events[0] != "e1" || events[1] != "e2" {
			t.Errorf("events = %v", events)
		}
	})

	t.Run("nested subcommand keeps the parent command name", func(t *testing.T) {
		adapter := &testAdapter{}
		code, stdout, _ := runApp(t, testApp(), adapter, "store", "reset")
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["command"] != "store" || envelope["sub"] != "reset" {
			t.Errorf("envelope = %v", envelope)
		}
	})

	t.Run("handler failure routes through ErrorResult with exit 1", func(t *testing.T) {
		adapter := &testAdapter{}
		code, stdout, stderr := runApp(t, testApp(), adapter, "wait", "--consumer", "fail", "u")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (stderr: %s)", code, stderr)
		}
		if !strings.Contains(stderr, "boom") {
			t.Errorf("stderr = %q, want the handler diagnostic", stderr)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatal(err)
		}
		errObj := envelope["error"].(map[string]any)
		if errObj["code"] != "internal" {
			t.Errorf("error code = %v, want internal", errObj["code"])
		}
	})

	t.Run("handler usage error exits 2 with usage envelope", func(t *testing.T) {
		adapter := &testAdapter{}
		code, _, _ := runApp(t, testApp(), adapter, "wait", "u")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	})

	t.Run("opt-in exit codes route through the adapter", func(t *testing.T) {
		adapter := &testAdapter{}
		code, _, _ := runApp(t, testApp(), adapter, "wait", "--consumer", "c", "--exit-on-events", "u")
		if code != 3 {
			t.Fatalf("exit code = %d, want 3", code)
		}
	})

	t.Run("common flags reach nested commands", func(t *testing.T) {
		adapter := &testAdapter{}
		code, _, _ := runApp(t, testApp(), adapter, "store", "path", "--state-dir", "/tmp/x")
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	})
}

func TestRunHelpPaths(t *testing.T) {
	adapter := &testAdapter{}
	for _, argv := range [][]string{{"--help"}, {"help"}, {"help", "reconcile"}, {"store", "--help"}, {"store", "reset", "--help"}, {"schema", "--help"}} {
		code, stdout, stderr := runApp(t, testApp(), adapter, argv...)
		if code != 0 {
			t.Fatalf("%v: exit code = %d, want 0 (stderr: %s)", argv, code, stderr)
		}
		if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
			t.Errorf("%v: help output is a JSON envelope", argv)
		}
	}
}

func TestRunHelpPathsWithVersion(t *testing.T) {
	adapter := &testAdapter{}
	_, flagHelp, _ := runApp(t, testApp(), adapter, "--help")
	code, cmdHelp, stderr := runApp(t, testApp(), adapter, "help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr)
	}
	if cmdHelp != flagHelp {
		t.Errorf("help output differs from --help output:\nhelp: %q\n--help: %q", cmdHelp, flagHelp)
	}
	if !strings.Contains(cmdHelp, "-v, --version") {
		t.Errorf("help output = %q, want the -v, --version line", cmdHelp)
	}
}

func TestRunTestSeamsCrash(t *testing.T) {
	adapter := &testAdapter{seams: &testSeams{crash: true}}
	defer func() {
		if r := recover(); r == nil {
			t.Error("BeforeWrite crash did not panic")
		}
	}()
	runApp(t, testApp(), adapter, "reconcile", "u")
}

func TestParse(t *testing.T) {
	t.Run("captures the invocation", func(t *testing.T) {
		inv, err := Parse(testApp(), []string{"ack", "--event=e1", "--event=e2", "u"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Command != "ack" || len(inv.Args) != 1 || inv.Args[0] != "u" || len(inv.Events) != 2 {
			t.Fatalf("inv = %+v", inv)
		}
	})

	t.Run("rejects caller errors", func(t *testing.T) {
		if _, err := Parse(testApp(), []string{"bogus"}); err == nil {
			t.Error("unknown command: err = nil, want a caller error")
		}
		if _, err := Parse(testApp(), []string{"reconcile"}); err == nil {
			t.Error("missing URL: err = nil, want a caller error")
		}
	})

	t.Run("raw commands yield their invocation", func(t *testing.T) {
		inv, err := Parse(testApp(), []string{"schema"})
		if err != nil {
			t.Fatalf("schema: %v", err)
		}
		if inv.Command != "schema" {
			t.Errorf("inv.Command = %q, want schema", inv.Command)
		}
	})
}

func TestSchemaDerivation(t *testing.T) {
	app := testApp()
	schemas := app.Schemas()

	reconcile, ok := schemas["reconcile"]
	if !ok {
		t.Fatal("schema missing reconcile")
	}
	if reconcile.Use != "reconcile <URL>" || !reconcile.URL {
		t.Errorf("reconcile entry = %+v", reconcile)
	}
	if _, ok := reconcile.Flags["head"]; !ok {
		t.Error("reconcile schema missing --head")
	}

	wait := schemas["wait"]
	if got := wait.Flags["timeout"].Default; got != "30m" {
		t.Errorf("wait --timeout default = %v, want 30m", got)
	}
	if got := wait.Flags["exit-on-events"].Kind; got != "bool" {
		t.Errorf("wait --exit-on-events kind = %q, want bool", got)
	}
	if got := schemas["ack"].Flags["event"].Kind; got != "repeat" {
		t.Errorf("ack --event kind = %q, want repeat", got)
	}

	store, ok := schemas["store"]
	if !ok || store.URL {
		t.Fatalf("store entry = %+v", store)
	}
	reset, ok := store.Subcommands["reset"]
	if !ok {
		t.Fatal("schema missing store reset")
	}
	if _, ok := reset.Flags["target"]; !ok {
		t.Error("store reset missing --target")
	}
	if _, ok := store.Subcommands["path"].Flags["target"]; ok {
		t.Error("store path exposes --target")
	}

	schema := schemas["schema"]
	if !ok || schema.URL {
		t.Errorf("schema entry = %+v", schema)
	}

	common := app.CommonFlagSchemas()
	if _, ok := common["state-dir"]; !ok {
		t.Error("common flags missing state-dir")
	}
}

// TestSchemaRendersShorthand pins that the schema document carries a declared
// shorthand and stays silent for a flag without one, so a consumer composing
// argv from the document can discover the short form.
func TestSchemaRendersShorthand(t *testing.T) {
	app := &App{
		Name: "t", Version: "v1",
		Commands: []*Command{{
			Name: "probe", Short: "run one probe",
			Flags: []Flag{
				{Name: "retries", Kind: Int, Short: "n", Usage: "retry attempts"},
				{Name: "target", Kind: String, Usage: "probe target"},
			},
			Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
				return nil, nil
			},
		}},
	}
	raw, err := json.Marshal(app.Schemas())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]struct {
		Flags map[string]map[string]any `json:"flags"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	flags := doc["probe"].Flags
	if got := flags["retries"]["short"]; got != "n" {
		t.Errorf("--retries short = %v, want n", got)
	}
	if got, ok := flags["target"]["short"]; ok {
		t.Errorf("--target short = %v, want the key absent", got)
	}
}

func TestValidatePanicsOnDrift(t *testing.T) {
	app := testApp()
	app.Commands = append(app.Commands, &Command{Name: "bogus", Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
		return nil, nil
	}})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("validate did not panic for a command without a short")
		}
		if !strings.Contains(r.(string), `command "bogus" has no help short`) {
			t.Errorf("panic = %v", r)
		}
	}()
	Run(app, &testAdapter{}, []string{"reconcile", "u"}, &bytes.Buffer{}, &bytes.Buffer{})
}

func TestValidateNamesTheParentOfANamelessSubcommand(t *testing.T) {
	app := &App{
		Name:    "ncli",
		Version: "vtest",
		Commands: []*Command{{
			Name:  "wf",
			Short: "workflow utilities",
			Subcommands: []*Command{{
				Short: "a subcommand with no name",
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return nil, nil
				},
			}},
		}},
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("validate did not panic for a subcommand without a name")
		}
		if !strings.Contains(r.(string), `command "wf" has a subcommand with no name`) {
			t.Errorf("panic = %v", r)
		}
	}()
	Run(app, &testAdapter{}, []string{"wf"}, &bytes.Buffer{}, &bytes.Buffer{})
}

// TestTreeValidatesTheDeclaration pins that Tree enforces the declaration
// contract like Run and Parse: an invalid declaration panics with validate's
// message instead of inside pflag at registration.
func TestTreeValidatesTheDeclaration(t *testing.T) {
	app := &App{
		Name: "t", Version: "v1",
		Commands: []*Command{{
			Name: "probe", Short: "run one probe",
			Flags: []Flag{
				{Name: "verbose", Kind: Bool, Short: "v", Usage: "log verbosely"},
				{Name: "vanilla", Kind: Bool, Short: "v", Usage: "log vanilla"},
			},
			Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
				return nil, nil
			},
		}},
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Tree did not validate the declaration")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, `flags "verbose" and "vanilla" share the "v" shorthand`) {
			t.Errorf("panic = %v, want validate's shorthand message", r)
		}
	}()
	app.Tree()
}

func TestDeadlineAndNotice(t *testing.T) {
	adapter := &testAdapter{notice: []string{"--x is deprecated"}}
	deadlineCalled := false
	adapter.deadline = func(ctx context.Context, inv Invocation) (context.Context, context.CancelFunc) {
		deadlineCalled = true
		return ctx, func() {}
	}
	code, _, stderr := runApp(t, testApp(), adapter, "reconcile", "u")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !deadlineCalled {
		t.Error("Deadline was not called before the handler")
	}
	if !strings.Contains(stderr, "--x is deprecated") {
		t.Errorf("stderr = %q, want the notice", stderr)
	}
}

func TestAfterWrite(t *testing.T) {
	app := testApp()
	adapter := &afterWriteAdapter{testAdapter: &testAdapter{}}
	code, _, stderr := runApp(t, app, adapter, "wait", "--consumer", "c", "--exit-on-events", "u")
	if code != 3 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr, "status=events") {
		t.Errorf("stderr = %q, want the post-write summary", stderr)
	}
}

type afterWriteAdapter struct{ *testAdapter }

func (a *afterWriteAdapter) AfterWrite(inv Invocation, result any, stderr io.Writer) {
	env := result.(map[string]any)
	stderr.Write([]byte("status=" + env["status"].(string) + "\n"))
}

func TestIntAndDurationFlags(t *testing.T) {
	app := &App{
		Name:    "t",
		Version: "v1",
		Commands: []*Command{
			{
				Name:  "job",
				Short: "run a job",
				Flags: []Flag{
					{Name: "retries", Kind: Int, Usage: "retry attempts"},
					{Name: "wait", Kind: Duration, Usage: "how long to wait"},
				},
				Handle: func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
					return map[string]any{
						"retries": inv.Ints["retries"],
						"wait":    inv.Durations["wait"].String(),
					}, nil
				},
			},
		},
	}

	code, stdout, stderr := runApp(t, app, &testAdapter{}, "job", "--retries=3", "--wait=90s")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	env := jsonField(t, stdout)
	if env["retries"] != float64(3) || env["wait"] != "1m30s" {
		t.Errorf("env = %v, want retries 3 and wait 1m30s", env)
	}

	// Defaults are zero when absent.
	code, stdout, _ = runApp(t, app, &testAdapter{}, "job")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	env = jsonField(t, stdout)
	if env["retries"] != float64(0) || env["wait"] != "0s" {
		t.Errorf("env = %v, want zero values", env)
	}

	// A bad duration is a caller error.
	if code, _, _ := runApp(t, app, &testAdapter{}, "job", "--wait=nope"); code != 2 {
		t.Errorf("exit = %d, want 2 for a bad duration", code)
	}
}

func TestPersistentFlags(t *testing.T) {
	app := &App{
		Name:    "t",
		Version: "v1",
		Commands: []*Command{
			{
				Name:  "wf",
				Short: "workflow parent",
				PersistentFlags: []Flag{
					{Name: "socket", Kind: String, Usage: "control socket path"},
				},
				Subcommands: []*Command{
					{
						Name:  "create",
						Short: "create a workflow",
						Handle: func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
							return map[string]any{"socket": inv.Flags["socket"]}, nil
						},
					},
				},
			},
		},
	}
	// The flag parses before the subcommand name.
	code, stdout, stderr := runApp(t, app, &testAdapter{}, "wf", "--socket", "/tmp/s", "create")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if got := jsonField(t, stdout)["socket"]; got != "/tmp/s" {
		t.Errorf("socket = %v", got)
	}
	// And after it, through normal flag parsing.
	code, stdout, _ = runApp(t, app, &testAdapter{}, "wf", "create", "--socket", "/tmp/s")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got := jsonField(t, stdout)["socket"]; got != "/tmp/s" {
		t.Errorf("socket = %v", got)
	}

	// The schema document renders persistent flags on the parent command.
	doc := app.Schemas()
	wf := doc["wf"]
	if len(wf.Flags) != 1 {
		t.Errorf("schema wf flags = %+v, want the persistent flag", wf.Flags)
	}
	if _, ok := wf.Flags["socket"]; !ok {
		t.Errorf("schema wf flags missing socket: %+v", wf.Flags)
	}
}

// jsonField decodes one JSON object from stdout.
func jsonField(t *testing.T, data string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("stdout is not one JSON value: %v (%q)", err, data)
	}
	return m
}

func TestDeclaredDefaultsApplyAtParse(t *testing.T) {
	app := &App{
		Name:    "t",
		Version: "v1",
		Commands: []*Command{
			{
				Name:  "job",
				Short: "run a job",
				Flags: []Flag{
					{Name: "retries", Kind: Int, Usage: "retry attempts", Default: 16},
					{Name: "wait", Kind: Duration, Usage: "how long", Default: 90 * time.Second},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{"retries": inv.Ints["retries"], "wait": inv.Durations["wait"].String()}, nil
				},
			},
		},
	}

	// Absent flags yield the declared default.
	code, stdout, _ := runApp(t, app, &testAdapter{}, "job")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	env := jsonField(t, stdout)
	if env["retries"] != float64(16) || env["wait"] != "1m30s" {
		t.Errorf("env = %v, want declared defaults", env)
	}

	// An explicitly-passed zero passes through as zero.
	code, stdout, _ = runApp(t, app, &testAdapter{}, "job", "--retries=0", "--wait=0s")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	env = jsonField(t, stdout)
	if env["retries"] != float64(0) || env["wait"] != "0s" {
		t.Errorf("env = %v, want explicit zeros", env)
	}
}

func TestSubcommandPositionals(t *testing.T) {
	app := &App{
		Name:    "t",
		Version: "v1",
		Commands: []*Command{
			{
				Name:  "ctl",
				Short: "parent",
				PersistentFlags: []Flag{
					{Name: "socket", Kind: String, Usage: "socket path"},
				},
				Subcommands: []*Command{
					{
						Name:  "show",
						Short: "show one runtime",
						Positionals: []Positional{
							{Name: "runtime-id"},
							{Name: "detail"},
						},
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{"args": inv.Args}, nil
						},
					},
					{
						Name:  "stop",
						Short: "no positionals",
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{}, nil
						},
					},
				},
			},
		},
	}
	// Declared positionals (required and optional) pass validation.
	code, stdout, stderr := runApp(t, app, &testAdapter{}, "ctl", "--socket", "/s", "show", "rt_1", "extra")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	env := jsonField(t, stdout)
	args := env["args"].([]any)
	if len(args) != 2 || args[0] != "rt_1" || args[1] != "extra" {
		t.Errorf("args = %v, want [rt_1 extra]", args)
	}

	// Beyond the declaration is still a caller error.
	if code, _, _ := runApp(t, app, &testAdapter{}, "ctl", "--socket", "/s", "show", "a", "b", "c"); code != 2 {
		t.Errorf("exit = %d, want 2 for an extra positional", code)
	}

	// A subcommand with no positionals still rejects arguments.
	if code, _, _ := runApp(t, app, &testAdapter{}, "ctl", "--socket", "/s", "stop", "x"); code != 2 {
		t.Errorf("exit = %d, want 2 for stop with an argument", code)
	}
}

func TestDeepNesting(t *testing.T) {
	app := &App{
		Name:    "t",
		Version: "v1",
		Commands: []*Command{
			{
				Name:  "wf",
				Short: "workflow root",
				PersistentFlags: []Flag{
					{Name: "socket", Kind: String, Usage: "socket path"},
				},
				Subcommands: []*Command{
					{
						Name:  "ctl",
						Short: "controller group",
						PersistentFlags: []Flag{
							{Name: "scope", Kind: String, Usage: "scope for controller verbs"},
						},
						Subcommands: []*Command{
							{
								Name:  "enable",
								Short: "enable one controller",
								Positionals: []Positional{
									{Name: "id", Required: true},
								},
								Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
									return map[string]any{
										"socket": inv.Flags["socket"],
										"scope":  inv.Flags["scope"],
										"id":     inv.Args[0],
										"path":   inv.Path,
									}, nil
								},
							},
						},
					},
					{
						Name:        "status",
						Short:       "flat subcommand still works",
						Positionals: []Positional{{Name: "id", Required: true}},
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{"sub": inv.Sub, "path": inv.Path}, nil
						},
					},
				},
			},
		},
	}

	// Three levels deep: persistent flags from both ancestors, positional
	// validation on the leaf, and the full path on the invocation.
	code, stdout, stderr := runApp(t, app, &testAdapter{},
		"wf", "--socket", "/s", "ctl", "--scope", "all", "enable", "ctrl_1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	env := jsonField(t, stdout)
	if env["socket"] != "/s" || env["scope"] != "all" || env["id"] != "ctrl_1" {
		t.Errorf("env = %v", env)
	}
	path := env["path"].([]any)
	if len(path) != 2 || path[0] != "ctl" || path[1] != "enable" {
		t.Errorf("path = %v, want [ctl enable]", path)
	}

	// The two-level shape is unchanged: Sub names the first nested level.
	code, stdout, _ = runApp(t, app, &testAdapter{}, "wf", "--socket", "/s", "status", "wf_1")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	env = jsonField(t, stdout)
	if env["sub"] != "status" {
		t.Errorf("sub = %v, want status", env["sub"])
	}

	// Bare inner node and unknown leaf are caller errors.
	if code, _, _ := runApp(t, app, &testAdapter{}, "wf", "--socket", "/s", "ctl"); code != 2 {
		t.Errorf("bare ctl: exit = %d, want 2", code)
	}
	if code, _, _ := runApp(t, app, &testAdapter{}, "wf", "--socket", "/s", "ctl", "bogus"); code != 2 {
		t.Errorf("unknown leaf: exit = %d, want 2", code)
	}
}

// typedFlagApp declares the flag kinds and command shapes the single-dash
// retry has to respect: typed flags with shorthands whose names never
// begin with their own shorthand letter (so `-name` fails the shorthand
// reading and becomes a rewrite candidate), a parent with a persistent
// flag, a NoInterspersed command, and common flags.
func typedFlagApp() *App {
	return &App{
		Name:      "toolcli",
		Version:   "vtest",
		RootShort: "toolcli exercises typed flags",
		CommonFlags: []Flag{
			{Name: "format", Kind: String, Usage: "output format"},
			{Name: "json", Kind: Bool, Usage: "emit JSON"},
		},
		Commands: []*Command{
			{
				Name:  "probe",
				Short: "run one probe",
				Positionals: []Positional{
					{Name: "target", Required: true},
					{Name: "extra", Variadic: true},
				},
				Flags: []Flag{
					{Name: "retries", Kind: Int, Short: "n", Usage: "probe retries"},
					{Name: "wait", Kind: Duration, Short: "d", Usage: "how long to wait"},
					{Name: "verbose", Kind: Bool, Short: "v", Usage: "log verbosely"},
					{Name: "brief", Kind: Bool, Short: "b", Usage: "log briefly"},
					{Name: "tag", Kind: Repeat, Short: "t", Usage: "record tags"},
					{Name: "agent", Kind: String, Usage: "agent name"},
					{Name: "label", Kind: String, Usage: "probe label"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{
						"retries": inv.Ints["retries"],
						"wait":    inv.Durations["wait"].String(),
						"verbose": inv.Bools["verbose"],
						"brief":   inv.Bools["brief"],
						"agent":   inv.Flags["agent"],
						"label":   inv.Flags["label"],
						"format":  inv.Flags["format"],
						"json":    inv.Bools["json"],
						"args":    inv.Args,
					}, nil
				},
			},
			{
				Name:  "remote",
				Short: "remote utilities",
				PersistentFlags: []Flag{
					{Name: "region", Kind: String, Usage: "target region"},
				},
				Subcommands: []*Command{
					{
						Name:  "list",
						Short: "list remotes",
						Flags: []Flag{{Name: "watch", Kind: Bool, Usage: "watch after listing"}},
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{
								"format": inv.Flags["format"],
								"region": inv.Flags["region"],
								"watch":  inv.Bools["watch"],
								"path":   inv.Path,
							}, nil
						},
					},
				},
			},
			{
				Name:           "scan",
				Short:          "scan fixed arguments",
				NoInterspersed: true,
				Positionals:    []Positional{{Name: "args", Required: true, Variadic: true}},
				Flags:          []Flag{{Name: "agent", Kind: String, Usage: "agent name"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{
						"args":   inv.Args,
						"agent":  inv.Flags["agent"],
						"format": inv.Flags["format"],
						"json":   inv.Bools["json"],
					}, nil
				},
			},
		},
	}
}

// flatLeaf declares one leaf of a flat app, taking the named string flags.
func flatLeaf(name string, flags ...string) *Command {
	declared := make([]Flag, len(flags))
	for i, f := range flags {
		declared[i] = Flag{Name: f, Kind: String, Usage: f}
	}
	return &Command{
		Name:  name,
		Short: name + " leaf",
		Flags: declared,
		Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
			return map[string]any{"flags": inv.Flags, "args": inv.Args}, nil
		},
	}
}

func TestSingleDashLongFlags(t *testing.T) {
	t.Run("string flag with separate and inline values", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-agent", "x", "-agent=y", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["agent"] != "y" {
			t.Errorf("agent = %q, want y", inv.Flags["agent"])
		}
	})

	t.Run("bool flag", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-verbose", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if !inv.Bools["verbose"] {
			t.Errorf("verbose = false, want true")
		}
	})

	t.Run("int and duration kinds", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-retries", "3", "-wait=2s", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Ints["retries"] != 3 || inv.Durations["wait"] != 2*time.Second {
			t.Errorf("retries = %d, wait = %v, want 3 and 2s", inv.Ints["retries"], inv.Durations["wait"])
		}
	})

	t.Run("persistent flag on a nested leaf", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"remote", "list", "-region", "eu"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["region"] != "eu" {
			t.Errorf("region = %q, want eu", inv.Flags["region"])
		}
	})

	t.Run("a common flag before the subcommand name", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"-format", "json", "probe", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["format"] != "json" {
			t.Errorf("format = %q, want json", inv.Flags["format"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})

	t.Run("a common bool flag before the subcommand name", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"-json", "probe", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if !inv.Bools["json"] {
			t.Errorf("json = false, want true")
		}
	})

	t.Run("a common flag after the subcommand name", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-format", "json", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["format"] != "json" {
			t.Errorf("format = %q, want json", inv.Flags["format"])
		}
	})

	t.Run("a flag-looking value stays the value", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-label", "-format", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["label"] != "-format" {
			t.Errorf("label = %q, want -format: the token was -label's value", inv.Flags["label"])
		}
		if inv.Flags["format"] != "" {
			t.Errorf("format = %q, want empty: the token was a value, not a flag", inv.Flags["format"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})

	t.Run("tokens after a bare double dash are untouched", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-agent", "x", "--", "tgt", "-bogus"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["agent"] != "x" {
			t.Errorf("agent = %q, want x", inv.Flags["agent"])
		}
		want := []string{"tgt", "-bogus"}
		if !slices.Equal(inv.Args, want) {
			t.Errorf("args = %v, want %v: tokens after -- are positionals", inv.Args, want)
		}
	})

	t.Run("NoInterspersed rewrites flags before the first positional", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"scan", "-agent", "x", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["agent"] != "x" {
			t.Errorf("agent = %q, want x", inv.Flags["agent"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})

	t.Run("NoInterspersed keeps dashes after the first positional across a retry", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"scan", "-agent", "x", "tgt", "-agent", "y"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["agent"] != "x" {
			t.Errorf("agent = %q, want x: the first -agent rewrote", inv.Flags["agent"])
		}
		want := []string{"tgt", "-agent", "y"}
		if !slices.Equal(inv.Args, want) {
			t.Errorf("args = %v, want %v: tokens after the first positional stay positionals", inv.Args, want)
		}
	})

	t.Run("a flag after a leaf's positional still rewrites", func(t *testing.T) {
		// tgt is off probe's command path, but probe is a leaf: its
		// segment runs to the end of argv, so the flag after the
		// positional still rewrites.
		inv, err := Parse(typedFlagApp(), []string{"probe", "tgt", "-label", "x"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["label"] != "x" {
			t.Errorf("label = %q, want x", inv.Flags["label"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})

	t.Run("a dash token before the command name neither descends nor ends the segment", func(t *testing.T) {
		// cobra's stripFlags reads neither "-" nor "" as a command
		// name, so the root's segment continues past them and probe's
		// own flag still rewrites. "-" is probe's positional.
		inv, err := Parse(typedFlagApp(), []string{"-", "probe", "-label", "x"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["label"] != "x" {
			t.Errorf("label = %q, want x", inv.Flags["label"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "-" {
			t.Errorf("args = %v, want [-]", inv.Args)
		}
	})

	t.Run("a resolved shorthand cluster keeps its meaning across a retry", func(t *testing.T) {
		// -vb parses as the bool shorthands v and b, so the retry must
		// not rewrite it to the long flag; -n 3's value must not be
		// mistaken for a flag either.
		inv, err := Parse(typedFlagApp(), []string{"probe", "-vb", "-n", "3", "-agent", "x", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if !inv.Bools["verbose"] || !inv.Bools["brief"] {
			t.Errorf("verbose = %v, brief = %v; want both true: the cluster keeps its meaning", inv.Bools["verbose"], inv.Bools["brief"])
		}
		if inv.Ints["retries"] != 3 || inv.Flags["agent"] != "x" {
			t.Errorf("retries = %d, agent = %q; want 3 and x", inv.Ints["retries"], inv.Flags["agent"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})

	t.Run("value-taking shorthands of every kind", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-n", "3", "-n4", "-d", "90s", "-d=1s", "-t", "a", "-tb", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Ints["retries"] != 4 {
			t.Errorf("retries = %d, want 4", inv.Ints["retries"])
		}
		if inv.Durations["wait"] != time.Second {
			t.Errorf("wait = %v, want 1s", inv.Durations["wait"])
		}
		if len(inv.Events) != 2 || inv.Events[0] != "a" || inv.Events[1] != "b" {
			t.Errorf("events = %v, want [a b]", inv.Events)
		}
	})

	t.Run("a root retry leaves a nested command's tokens alone", func(t *testing.T) {
		// The first pass fails at the root, so only the root's own
		// segment rewrites; the trailing -format is scan's positional.
		inv, err := Parse(typedFlagApp(), []string{"-format", "json", "scan", "tgt", "-format"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["format"] != "json" {
			t.Errorf("format = %q, want json", inv.Flags["format"])
		}
		want := []string{"tgt", "-format"}
		if !slices.Equal(inv.Args, want) {
			t.Errorf("args = %v, want %v: the tail belongs to scan", inv.Args, want)
		}
	})

	t.Run("a root retry leaves a nested command's flag value alone", func(t *testing.T) {
		// -json is the value of probe's --label, not a root flag to
		// rewrite: the root's segment ends at the subcommand name.
		inv, err := Parse(typedFlagApp(), []string{"-format", "json", "probe", "--label", "-json", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["label"] != "-json" {
			t.Errorf("label = %q, want -json: the token was --label's value", inv.Flags["label"])
		}
		if inv.Bools["json"] {
			t.Errorf("json = true, want false: -json never parsed as a flag")
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})

	t.Run("a leaf retry ends where cobra took the command name", func(t *testing.T) {
		// The first pass resolves probe and fails on -format. Its value
		// slot swallows the token cobra read as the command name, so
		// "scan" is the first positional off probe's command path: the
		// leaf's segment ends there, and the next pass resolves the
		// command from cobra's own reading.
		inv, err := Parse(typedFlagApp(), []string{"-format", "probe", "scan", "tgt", "-json"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Command != "scan" {
			t.Errorf("command = %q, want scan", inv.Command)
		}
		if inv.Flags["format"] != "probe" {
			t.Errorf("format = %q, want probe", inv.Flags["format"])
		}
		if inv.Bools["json"] {
			t.Errorf("json = true, want false: -json comes after scan's first positional")
		}
		want := []string{"tgt", "-json"}
		if !slices.Equal(inv.Args, want) {
			t.Errorf("args = %v, want %v: the same as --format probe scan tgt -json", inv.Args, want)
		}
	})

	t.Run("each pass rewrites the command segment cobra failed on", func(t *testing.T) {
		// Three failing passes: the root resolves the unknown command,
		// the remote group resolves the failing flag, and list resolves
		// its own. Each pass rewrites only the segment of the command
		// that failed.
		inv, err := Parse(typedFlagApp(), []string{"-format", "json", "remote", "-region", "eu", "list", "-watch"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Command != "remote" || inv.Sub != "list" {
			t.Errorf("command = %q, sub = %q; want remote and list", inv.Command, inv.Sub)
		}
		if want := []string{"list"}; !slices.Equal(inv.Path, want) {
			t.Errorf("path = %v, want %v", inv.Path, want)
		}
		if inv.Flags["format"] != "json" || inv.Flags["region"] != "eu" {
			t.Errorf("format = %q, region = %q; want json and eu", inv.Flags["format"], inv.Flags["region"])
		}
		if !inv.Bools["watch"] {
			t.Errorf("watch = false, want true")
		}
		if len(inv.Args) != 0 {
			t.Errorf("args = %v, want none", inv.Args)
		}
	})

	t.Run("a one-letter long flag rewrites like any other", func(t *testing.T) {
		// -x is a long flag, not a shorthand, wherever the command
		// declares no "x" shorthand: the rewrite reads it like any
		// longer name.
		app := &App{Name: "flat", Commands: []*Command{
			flatLeaf("a", "x"),
			flatLeaf("b", "x", "y"),
			flatLeaf("c", "x", "y", "z"),
		}}
		want, err := Parse(app, []string{"--x", "a", "--y", "b", "c", "--z", "v"})
		if err != nil {
			t.Fatal(err)
		}
		inv, err := Parse(app, []string{"-x", "a", "-y", "b", "c", "-z", "v"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Command != "c" || inv.Flags["z"] != "v" {
			t.Errorf("command = %q, z = %q; want c and v", inv.Command, inv.Flags["z"])
		}
		if inv.Command != want.Command || !maps.Equal(inv.Flags, want.Flags) || !slices.Equal(inv.Args, want.Args) {
			t.Errorf("single dash = %+v, want the double-dash spelling's %+v", inv, want)
		}
	})

	t.Run("sibling leaves each swallow the next name across passes", func(t *testing.T) {
		// Each pass rewrites the leaf cobra resolved, and the rewritten
		// flag then takes a value: it swallows a command name, so the
		// next pass resolves the next sibling. The chain is longer than
		// the flat app has levels.
		app := &App{Name: "flat", Commands: []*Command{
			flatLeaf("one", "xy"),
			flatLeaf("two", "xy", "zw"),
			flatLeaf("three", "xy", "zw", "uv"),
			flatLeaf("four", "xy", "zw", "uv"),
		}}
		inv, err := Parse(app, []string{"-xy", "one", "-zw", "two", "-uv", "three", "four"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Command != "four" {
			t.Errorf("command = %q, want four", inv.Command)
		}
		want := map[string]string{"xy": "one", "zw": "two", "uv": "three"}
		if !maps.Equal(inv.Flags, want) {
			t.Errorf("flags = %v, want %v", inv.Flags, want)
		}
	})

	t.Run("an undeclared single-dash token fails as a caller error", func(t *testing.T) {
		_, err := Parse(typedFlagApp(), []string{"probe", "-bogus", "tgt"})
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Fatalf("err = %v, want a UsageError", err)
		}
	})
}

func TestSingleDashLongFlagsOnRun(t *testing.T) {
	t.Run("typed flags dispatch through the retry", func(t *testing.T) {
		code, stdout, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "probe", "-retries", "3", "-wait=2s", "-verbose", "tgt")
		if code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, stderr)
		}
		env := jsonField(t, stdout)
		if env["retries"] != float64(3) || env["wait"] != "2s" || env["verbose"] != true {
			t.Errorf("env = %v, want retries 3, wait 2s, verbose true", env)
		}
	})

	t.Run("a flag after a positional dispatches through the retry", func(t *testing.T) {
		code, stdout, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "probe", "tgt", "-label", "x")
		if code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, stderr)
		}
		env := jsonField(t, stdout)
		if env["label"] != "x" {
			t.Errorf("label = %v, want x", env["label"])
		}
		args, _ := env["args"].([]any)
		if len(args) != 1 || args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", args)
		}
	})

	t.Run("-help on a subcommand rewrites to the help flag", func(t *testing.T) {
		code, stdout, _ := runApp(t, typedFlagApp(), &testAdapter{}, "probe", "-help")
		if code != 0 {
			t.Errorf("exit = %d, want 0 for the probe -help help path", code)
		}
		if strings.HasPrefix(stdout, "{") {
			t.Errorf("stdout = %q, want plain help text", stdout)
		}
	})

	t.Run("-help on the root rewrites to the help flag", func(t *testing.T) {
		var rootHelp string
		_, rootHelp, _ = runApp(t, typedFlagApp(), &testAdapter{}, "--help")
		code, stdout, _ := runApp(t, typedFlagApp(), &testAdapter{}, "-help")
		if code != 0 || stdout != rootHelp {
			t.Errorf("-help: exit = %d, want the root help and exit 0", code)
		}
	})

	t.Run("-version on the root rewrites to the version flag", func(t *testing.T) {
		code, stdout, _ := runApp(t, typedFlagApp(), &testAdapter{}, "-version")
		if code != 0 || stdout != "toolcli vtest\n" {
			t.Errorf("-version: exit = %d, stdout = %q; want the version and exit 0", code, stdout)
		}
	})

	t.Run("-version before a subcommand name still fails", func(t *testing.T) {
		// The version flag belongs to the root; the resolved probe has
		// no such long flag, so nothing rewrites and the original error
		// stands.
		code, _, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "-version", "probe")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if want := "probe: unknown shorthand flag: 'e' in -ersion\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("a leaf retry dispatches the command cobra resolved", func(t *testing.T) {
		code, stdout, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "-format", "probe", "scan", "tgt", "-json")
		if code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, stderr)
		}
		env := jsonField(t, stdout)
		if env["format"] != "probe" {
			t.Errorf("format = %v, want probe", env["format"])
		}
		if env["json"] != false {
			t.Errorf("json = %v, want false: -json comes after scan's first positional", env["json"])
		}
		args, _ := env["args"].([]any)
		if len(args) != 2 || args[0] != "tgt" || args[1] != "-json" {
			t.Errorf("args = %v, want [tgt -json]: the same as --format probe scan tgt -json", args)
		}
	})

	t.Run("each pass rewrites the command segment cobra failed on", func(t *testing.T) {
		code, stdout, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "-format", "json", "remote", "-region", "eu", "list", "-watch")
		if code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, stderr)
		}
		env := jsonField(t, stdout)
		if env["format"] != "json" || env["region"] != "eu" || env["watch"] != true {
			t.Errorf("env = %v, want format json, region eu, watch true", env)
		}
		path, _ := env["path"].([]any)
		if len(path) != 1 || path[0] != "list" {
			t.Errorf("path = %v, want [list]", path)
		}
	})
}

// TestSingleDashCompatibility pins argv that parsed under the previous
// release: they must keep parsing identically, because the retry only runs
// after a failed parse.
func TestSingleDashCompatibility(t *testing.T) {
	t.Run("a flag-looking value keeps its bytes", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"--label", "-format", "probe", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["label"] != "-format" {
			t.Errorf("label = %q, want -format", inv.Flags["label"])
		}
		if inv.Flags["format"] != "" {
			t.Errorf("format = %q, want empty: -format was a value", inv.Flags["format"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})

	t.Run("NoInterspersed passthrough stays byte-identical", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"scan", "tgt", "-format"})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"tgt", "-format"}
		if !slices.Equal(inv.Args, want) {
			t.Errorf("args = %v, want %v: passthrough tokens must stay byte-identical", inv.Args, want)
		}
	})

	t.Run("a value-taking shorthand absorbs the next token", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-t", "-agent", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if len(inv.Events) != 1 || inv.Events[0] != "-agent" {
			t.Errorf("events = %v, want [-agent]: the token was -t's value", inv.Events)
		}
		if inv.Flags["agent"] != "" {
			t.Errorf("agent = %q, want empty", inv.Flags["agent"])
		}
	})

	t.Run("--version before a subcommand name prints the version", func(t *testing.T) {
		code, stdout, _ := runApp(t, typedFlagApp(), &testAdapter{}, "--version", "probe")
		if code != 0 || stdout != "toolcli vtest\n" {
			t.Errorf("exit = %d, stdout = %q; want the version and exit 0", code, stdout)
		}
	})

	t.Run("-h before a subcommand name prints the root help", func(t *testing.T) {
		var rootHelp string
		_, rootHelp, _ = runApp(t, typedFlagApp(), &testAdapter{}, "--help")
		code, stdout, _ := runApp(t, typedFlagApp(), &testAdapter{}, "-h", "probe")
		if code != 0 || stdout != rootHelp {
			t.Errorf("-h probe: exit = %d, want the root help and exit 0", code)
		}
	})

	t.Run("bool shorthands beat the long spelling of the same letters", func(t *testing.T) {
		app := &App{
			Name:    "scli",
			Version: "vtest",
			Commands: []*Command{{
				Name:        "run",
				Short:       "run one job",
				Positionals: []Positional{{Name: "args", Variadic: true}},
				Flags: []Flag{
					{Name: "verbose", Kind: Bool, Short: "v", Usage: "log verbosely"},
					{Name: "brief", Kind: Bool, Short: "b", Usage: "log briefly"},
					{Name: "vb", Kind: String, Usage: "versioned build id"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{
						"verbose": inv.Bools["verbose"],
						"brief":   inv.Bools["brief"],
						"vb":      inv.Flags["vb"],
						"args":    inv.Args,
					}, nil
				},
			}},
		}
		inv, err := Parse(app, []string{"run", "-vb", "x"})
		if err != nil {
			t.Fatal(err)
		}
		if !inv.Bools["verbose"] || !inv.Bools["brief"] {
			t.Errorf("verbose = %v, brief = %v; want both true: the shorthand reading keeps its meaning", inv.Bools["verbose"], inv.Bools["brief"])
		}
		if inv.Flags["vb"] != "" {
			t.Errorf("vb = %q, want empty: the token was the shorthand cluster", inv.Flags["vb"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "x" {
			t.Errorf("args = %v, want [x]", inv.Args)
		}
	})

	t.Run("an equals value inside a bool cluster keeps the shorthand reading", func(t *testing.T) {
		// pflag reads -vb=false as v=true, b=false. With a long flag vb
		// also declared, a retry that rewrote the token would give vb the
		// value "false" instead.
		app := &App{
			Name:    "scli",
			Version: "vtest",
			Commands: []*Command{{
				Name:  "run",
				Short: "run one job",
				Flags: []Flag{
					{Name: "verbose", Kind: Bool, Short: "v", Usage: "log verbosely"},
					{Name: "brief", Kind: Bool, Short: "b", Usage: "log briefly"},
					{Name: "vb", Kind: String, Usage: "versioned build id"},
					{Name: "agent", Kind: String, Usage: "agent name"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{
						"verbose": inv.Bools["verbose"],
						"brief":   inv.Bools["brief"],
						"vb":      inv.Flags["vb"],
						"agent":   inv.Flags["agent"],
					}, nil
				},
			}},
		}
		// -agent fails the shorthand reading and triggers the retry; the
		// bool cluster before it must keep its meaning.
		inv, err := Parse(app, []string{"run", "-vb=false", "-agent", "x"})
		if err != nil {
			t.Fatal(err)
		}
		if !inv.Bools["verbose"] || inv.Bools["brief"] {
			t.Errorf("verbose = %v, brief = %v; want true and false", inv.Bools["verbose"], inv.Bools["brief"])
		}
		if inv.Flags["vb"] != "" {
			t.Errorf("vb = %q, want empty: the token was the shorthand cluster", inv.Flags["vb"])
		}
		if inv.Flags["agent"] != "x" {
			t.Errorf("agent = %q, want x", inv.Flags["agent"])
		}
	})
}

// TestSingleDashRetryDispatch pins that a retry which reached dispatch owns
// its outcome: the dispatched command's failure reaches the caller instead
// of the flag error that triggered the retry, and each handler runs once.
type countingAdapter struct {
	testAdapter
	before int
}

func (a *countingAdapter) BeforeHandler(inv Invocation, stderr io.Writer) {
	a.before++
	a.testAdapter.BeforeHandler(inv, stderr)
}

// validateErrAdapter fails in the handler-phase validation, the step after a
// pass has claimed dispatch.
type validateErrAdapter struct {
	testAdapter
	err error
}

func (a *validateErrAdapter) Validate(inv Invocation) error { return a.err }

func TestSingleDashRetryDispatch(t *testing.T) {
	t.Run("a raw command's error survives the retry that reached it", func(t *testing.T) {
		runs := 0
		app := &App{
			Name:    "rawcli",
			Version: "vtest",
			Commands: []*Command{{
				Name:  "emit",
				Short: "emit one record",
				Flags: []Flag{{Name: "agent", Kind: String, Usage: "agent name"}},
				Raw: func(ctx context.Context, inv Invocation, stdout io.Writer) error {
					runs++
					stdout.Write([]byte("raw output\n"))
					return errors.New("raw failed")
				},
			}},
		}
		code, stdout, stderr := runApp(t, app, &testAdapter{}, "emit", "-agent", "x")
		if code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
		if want := "raw failed\n"; stderr != want {
			t.Errorf("stderr = %q, want %q: the raw command's own error", stderr, want)
		}
		if !strings.Contains(stdout, "raw output") {
			t.Errorf("stdout = %q, want the raw command's output", stdout)
		}
		if runs != 1 {
			t.Errorf("raw ran %d times, want 1", runs)
		}
	})

	t.Run("the handler and BeforeHandler each run exactly once", func(t *testing.T) {
		handles := 0
		app := &App{
			Name:    "hccli",
			Version: "vtest",
			Commands: []*Command{{
				Name:  "probe",
				Short: "run one probe",
				Flags: []Flag{{Name: "agent", Kind: String, Usage: "agent name"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					handles++
					return map[string]any{"agent": inv.Flags["agent"]}, nil
				},
			}},
		}
		adapter := &countingAdapter{}
		code, _, stderr := runApp(t, app, adapter, "probe", "-agent", "x")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
		}
		if handles != 1 {
			t.Errorf("handler ran %d times, want 1: the first pass failed before dispatch", handles)
		}
		if adapter.before != 1 {
			t.Errorf("BeforeHandler ran %d times, want 1", adapter.before)
		}
	})

	t.Run("a handler's error survives the retry that reached it", func(t *testing.T) {
		runs := 0
		app := &App{
			Name:    "hccli",
			Version: "vtest",
			Commands: []*Command{{
				Name:  "probe",
				Short: "run one probe",
				Flags: []Flag{{Name: "agent", Kind: String, Usage: "agent name"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					runs++
					return nil, errors.New("probe failed")
				},
			}},
		}
		code, _, stderr := runApp(t, app, &testAdapter{}, "probe", "-agent", "x")
		if code != 1 {
			t.Errorf("exit = %d, want 1: the handler failed operationally", code)
		}
		if want := "probe failed\n"; stderr != want {
			t.Errorf("stderr = %q, want %q: the handler's own error", stderr, want)
		}
		if runs != 1 {
			t.Errorf("handler ran %d times, want 1", runs)
		}
	})

	t.Run("an adapter failure after dispatch is not masked by the flag error", func(t *testing.T) {
		// The second pass dispatches and its validation fails. The pass
		// owns its outcome, so its error is what the caller sees rather
		// than the unknown-shorthand error that triggered the retry.
		app := &App{
			Name:    "hccli",
			Version: "vtest",
			Commands: []*Command{{
				Name:  "probe",
				Short: "run one probe",
				Flags: []Flag{{Name: "agent", Kind: String, Usage: "agent name"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{"agent": inv.Flags["agent"]}, nil
				},
			}},
		}
		adapter := &validateErrAdapter{err: errors.New("probe rejected")}
		_, _, stderr := runApp(t, app, adapter, "probe", "-agent", "x")
		if want := "probe rejected\n"; stderr != want {
			t.Errorf("stderr = %q, want %q: the dispatched pass's own error", stderr, want)
		}
	})
}

// TestSingleDashFallback pins the other side of the precedence: with the
// v and b shorthands undeclared, -vb fails the shorthand reading and the
// retry reads it as the long flag.
func TestSingleDashFallback(t *testing.T) {
	app := &App{
		Name:    "scli",
		Version: "vtest",
		Commands: []*Command{{
			Name:  "run",
			Short: "run one job",
			Flags: []Flag{
				{Name: "vb", Kind: String, Usage: "versioned build id"},
			},
			Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
				return map[string]any{"vb": inv.Flags["vb"], "args": inv.Args}, nil
			},
		}},
	}
	inv, err := Parse(app, []string{"run", "-vb", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Flags["vb"] != "x" {
		t.Errorf("vb = %q, want x: the long flag applies when the shorthand reading fails", inv.Flags["vb"])
	}
	if len(inv.Args) != 0 {
		t.Errorf("args = %v, want none: x was the flag's value", inv.Args)
	}
}

// TestSingleDashErrors pins the original-error promise: a single-dash token
// that is not a declared long flag, or a retry that also fails, reports the
// first parse's error with the usual exit path.
func TestSingleDashErrors(t *testing.T) {
	t.Run("an undeclared token reports the original error", func(t *testing.T) {
		code, _, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "probe", "-bogus", "tgt")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if want := "probe: unknown shorthand flag: 'o' in -ogus\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("a retry that also fails reports the original error", func(t *testing.T) {
		// -agent rewrites, but -bogus still fails after it: the caller
		// sees the first parse's error, not the retry's.
		code, _, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "probe", "-agent", "x", "-bogus", "tgt")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if want := "probe: unknown shorthand flag: 'a' in -agent\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("a rewritten common flag whose retry fails reports the original error", func(t *testing.T) {
		code, _, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "-format", "json", "bogus", "probe", "tgt")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if want := "unknown command \"json\" for \"toolcli\"\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("a leaf flag before its subcommand name still fails", func(t *testing.T) {
		code, _, stderr := runApp(t, typedFlagApp(), &testAdapter{}, "-region", "eu", "remote", "list")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if want := "unknown command \"eu\" for \"toolcli\"\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})
}

// TestSuppliedFlags pins Invocation.Supplied: the names of flags given on
// the command line, covering every kind and inherited persistent and
// common flags. A defaulted flag that was not typed keeps its default value
// and stays absent from the map, so an explicit zero is distinguishable
// from absent.
func TestSuppliedFlags(t *testing.T) {
	var handlerInv Invocation
	app := &App{
		Name:    "scli",
		Version: "vtest",
		CommonFlags: []Flag{
			{Name: "format", Kind: String, Usage: "output format"},
		},
		Commands: []*Command{{
			Name:  "job",
			Short: "run a job",
			Flags: []Flag{
				{Name: "name", Kind: String, Usage: "job name"},
				{Name: "retries", Kind: Int, Usage: "retry attempts", Default: 16},
				{Name: "verbose", Kind: Bool, Short: "v", Usage: "log verbosely"},
				{Name: "wait", Kind: Duration, Usage: "how long to wait"},
				{Name: "tag", Kind: Repeat, Usage: "record tags"},
			},
			Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
				handlerInv = inv
				return map[string]any{"status": "ok"}, nil
			},
		}},
	}

	parse := func(t *testing.T, argv ...string) Invocation {
		t.Helper()
		inv, err := Parse(app, argv)
		if err != nil {
			t.Fatal(err)
		}
		return inv
	}

	t.Run("an explicit empty value is supplied", func(t *testing.T) {
		inv := parse(t, "job", "--name", "")
		if _, ok := inv.Supplied["name"]; !ok {
			t.Error("Supplied[name] is absent, want present: the flag was typed")
		}
		if inv.Flags["name"] != "" {
			t.Errorf("Flags[name] = %q, want the explicit empty value", inv.Flags["name"])
		}
	})

	t.Run("an omitted flag with a declared default is absent", func(t *testing.T) {
		inv := parse(t, "job")
		if _, ok := inv.Supplied["retries"]; ok {
			t.Error("Supplied[retries] is present, want absent: the flag was not typed")
		}
		if inv.Ints["retries"] != 16 {
			t.Errorf("retries = %d, want the declared default 16", inv.Ints["retries"])
		}
	})

	t.Run("an explicitly-passed default value is supplied", func(t *testing.T) {
		inv := parse(t, "job", "--retries", "16")
		if _, ok := inv.Supplied["retries"]; !ok {
			t.Error("Supplied[retries] is absent, want present: the flag was typed")
		}
		if inv.Ints["retries"] != 16 {
			t.Errorf("retries = %d, want 16", inv.Ints["retries"])
		}
	})

	t.Run("a bool flag given as its long name", func(t *testing.T) {
		inv := parse(t, "job", "--verbose")
		if _, ok := inv.Supplied["verbose"]; !ok {
			t.Error("Supplied[verbose] is absent, want present for --verbose")
		}
		if !inv.Bools["verbose"] {
			t.Error("verbose = false, want true")
		}
	})

	t.Run("a bool flag given as its shorthand", func(t *testing.T) {
		inv := parse(t, "job", "-v")
		if _, ok := inv.Supplied["verbose"]; !ok {
			t.Error("Supplied[verbose] is absent, want present for -v")
		}
	})

	t.Run("a repeat flag collects every value", func(t *testing.T) {
		inv := parse(t, "job", "--tag", "a", "--tag=b")
		if _, ok := inv.Supplied["tag"]; !ok {
			t.Error("Supplied[tag] is absent, want present")
		}
		if len(inv.Events) != 2 || inv.Events[0] != "a" || inv.Events[1] != "b" {
			t.Errorf("events = %v, want [a b]", inv.Events)
		}
	})

	t.Run("a common flag given before the subcommand", func(t *testing.T) {
		inv := parse(t, "--format", "json", "job")
		if _, ok := inv.Supplied["format"]; !ok {
			t.Error("Supplied[format] is absent, want present")
		}
		if inv.Flags["format"] != "json" {
			t.Errorf("format = %q, want json", inv.Flags["format"])
		}
	})

	t.Run("a single-dash common flag reached through a retry", func(t *testing.T) {
		inv := parse(t, "-format", "json", "job")
		if _, ok := inv.Supplied["format"]; !ok {
			t.Error("Supplied[format] is absent, want present after the retry")
		}
		if inv.Flags["format"] != "json" {
			t.Errorf("format = %q, want json", inv.Flags["format"])
		}
	})

	t.Run("a duration flag", func(t *testing.T) {
		inv := parse(t, "job", "--wait", "3s")
		if _, ok := inv.Supplied["wait"]; !ok {
			t.Error("Supplied[wait] is absent, want present for --wait")
		}
		if inv.Durations["wait"] != 3*time.Second {
			t.Errorf("wait = %v, want 3s", inv.Durations["wait"])
		}
	})

	t.Run("a repeated bool keeps the last value", func(t *testing.T) {
		inv := parse(t, "job", "--verbose", "--verbose=false")
		if _, ok := inv.Supplied["verbose"]; !ok {
			t.Error("Supplied[verbose] is absent, want present")
		}
		if inv.Bools["verbose"] {
			t.Error("verbose = true, want false: the last value wins")
		}
	})

	t.Run("the handler sees the same Supplied as Parse", func(t *testing.T) {
		argv := []string{"--format", "json", "job", "-v", "--name", "x", "--tag", "t1", "--tag=t2"}
		if code, _, _ := runApp(t, app, &testAdapter{}, argv...); code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		parsed := parse(t, argv...)
		if !maps.Equal(handlerInv.Supplied, parsed.Supplied) {
			t.Errorf("handler Supplied = %v, want Parse's %v", handlerInv.Supplied, parsed.Supplied)
		}
	})

	t.Run("a persistent flag given on a nested leaf", func(t *testing.T) {
		nested := &App{
			Name:    "ncli",
			Version: "vtest",
			Commands: []*Command{{
				Name:  "wf",
				Short: "workflow utilities",
				PersistentFlags: []Flag{
					{Name: "region", Kind: String, Usage: "target region"},
				},
				Subcommands: []*Command{{
					Name:  "list",
					Short: "list workflows",
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
						return nil, nil
					},
				}},
			}},
		}
		inv, err := Parse(nested, []string{"wf", "list", "--region", "eu"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := inv.Supplied["region"]; !ok {
			t.Error("Supplied[region] is absent, want present")
		}
		if inv.Flags["region"] != "eu" {
			t.Errorf("region = %q, want eu", inv.Flags["region"])
		}
	})
}

// TestShorthandParsesEveryKind pins that Short registers for every flag kind:
// one argv carries an Int, a Duration, a Repeat and a String in short form.
func TestShorthandParsesEveryKind(t *testing.T) {
	app := &App{
		Name:    "t",
		Version: "v1",
		Commands: []*Command{{
			Name:  "job",
			Short: "run a job",
			Flags: []Flag{
				{Name: "retries", Kind: Int, Short: "n", Usage: "retry attempts"},
				{Name: "wait", Kind: Duration, Short: "w", Usage: "how long to wait"},
				{Name: "tag", Kind: Repeat, Short: "t", Usage: "record tags"},
				{Name: "output", Kind: String, Short: "o", Usage: "output path"},
			},
			Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
				return map[string]any{"status": "ok"}, nil
			},
		}},
	}
	inv, err := Parse(app, []string{"job", "-n", "3", "-w", "3s", "-t", "a", "-t", "b", "-o", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Ints["retries"] != 3 {
		t.Errorf("retries = %d, want 3 from -n", inv.Ints["retries"])
	}
	if inv.Durations["wait"] != 3*time.Second {
		t.Errorf("wait = %v, want 3s from -w", inv.Durations["wait"])
	}
	if len(inv.Events) != 2 || inv.Events[0] != "a" || inv.Events[1] != "b" {
		t.Errorf("events = %v, want [a b] from -t", inv.Events)
	}
	if inv.Flags["output"] != "x" {
		t.Errorf("output = %q, want x from -o", inv.Flags["output"])
	}
	for _, name := range []string{"retries", "wait", "tag", "output"} {
		if _, ok := inv.Supplied[name]; !ok {
			t.Errorf("Supplied[%s] is absent, want present", name)
		}
	}
}

// expectUsagePanic pins the library's validation failure for one invalid
// declaration: Run panics with a message containing want, not with a pflag
// panic from flag registration or from cobra's help flag.
func expectUsagePanic(t *testing.T, app *App, argv []string, want string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("validate did not reject the declaration")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, want) {
			t.Errorf("panic = %v, want it to contain %q", r, want)
		}
	}()
	Run(app, &testAdapter{}, argv, &bytes.Buffer{}, &bytes.Buffer{})
}

// TestValidateRejectsShorthandCollisions pins the shorthand validation:
// a multi-character shorthand, and two flags that share a shorthand across
// a command's merged flag set, fail at startup as UsageErrors instead of
// panicking inside pflag at registration. A flag that shadows a same-named
// one keeps its shorthand.
func TestValidateRejectsShorthandCollisions(t *testing.T) {
	t.Run("two flags of one command sharing a shorthand", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Flags: []Flag{
					{Name: "verbose", Kind: Bool, Short: "v", Usage: "log verbosely"},
					{Name: "vanilla", Kind: Bool, Short: "v", Usage: "log vanilla"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, []string{"probe", "u"}, `flags "verbose" and "vanilla" share the "v" shorthand`)
	})

	t.Run("two common flags sharing a shorthand", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			CommonFlags: []Flag{
				{Name: "format", Kind: String, Short: "f", Usage: "output format"},
				{Name: "filter", Kind: String, Short: "f", Usage: "filter expression"},
			},
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, []string{"probe", "u"}, `common flags "format" and "filter" share the "f" shorthand`)
	})

	t.Run("a common flag and a command flag sharing a shorthand", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			CommonFlags: []Flag{{Name: "format", Kind: String, Short: "f", Usage: "output format"}},
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Flags:  []Flag{{Name: "filter", Kind: String, Short: "f", Usage: "filter expression"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, []string{"probe", "u"}, `flags "format" and "filter" share the "f" shorthand`)
	})

	t.Run("an inherited persistent flag and a leaf flag sharing a shorthand", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow utilities",
				PersistentFlags: []Flag{{Name: "page", Kind: Int, Short: "p", Usage: "page size"}},
				Subcommands: []*Command{{
					Name: "list", Short: "list workflows",
					Flags:  []Flag{{Name: "pretty", Kind: Bool, Short: "p", Usage: "print pretty"}},
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
				}},
			}},
		}, []string{"wf", "list"}, `flags "page" and "pretty" share the "p" shorthand`)
	})

	t.Run("persistent flags of two ancestors sharing a shorthand", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow utilities",
				PersistentFlags: []Flag{{Name: "page", Kind: Int, Short: "p", Usage: "page size"}},
				Subcommands: []*Command{{
					Name: "ctl", Short: "controller group",
					PersistentFlags: []Flag{{Name: "pretty", Kind: Bool, Short: "p", Usage: "print pretty"}},
					Subcommands: []*Command{{
						Name: "enable", Short: "enable one controller",
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
					}},
				}},
			}},
		}, []string{"wf", "ctl", "enable"}, `command "wf ctl" flags "page" and "pretty" share the "p" shorthand`)
	})

	t.Run("a multi-character shorthand", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Flags:  []Flag{{Name: "verbose", Kind: Bool, Short: "vv", Usage: "log verbosely"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, []string{"probe", "u"}, `flag "verbose" uses shorthand "vv", want one letter`)
	})

	t.Run("a same-named flag shadows with the same shorthand", func(t *testing.T) {
		app := &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow utilities",
				PersistentFlags: []Flag{{Name: "page", Kind: Int, Short: "p", Usage: "page size"}},
				Subcommands: []*Command{{
					Name: "list", Short: "list workflows",
					Flags: []Flag{{Name: "page", Kind: Int, Short: "p", Usage: "page size"}},
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
						return map[string]any{"status": "ok"}, nil
					},
				}},
			}},
		}
		code, _, stderr := runApp(t, app, &testAdapter{}, "wf", "list")
		if code != 0 {
			t.Errorf("shadowed shorthand: exit = %d, stderr = %s; want a clean run", code, stderr)
		}
	})
}

// TestValidateRejectsKindMismatchedShadow pins the shadow rule: a flag that
// takes an inherited flag's name must agree with its kind, because pflag keeps
// only the shadow's value while the invocation reads the inherited flag with
// the kind it was declared with. A same-kind shadow still parses the leaf's
// own value.
func TestValidateRejectsKindMismatchedShadow(t *testing.T) {
	// shadowApp declares --page on the "wf" parent and shadows it under
	// "wf list" with leaf.
	shadowApp := func(leaf Flag) *App {
		return &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow utilities",
				PersistentFlags: []Flag{{Name: "page", Kind: Int, Usage: "page size"}},
				Subcommands: []*Command{{
					Name: "list", Short: "list workflows",
					Flags: []Flag{leaf},
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
						return map[string]any{"status": "ok"}, nil
					},
				}},
			}},
		}
	}

	t.Run("a shadow of a different kind fails at startup", func(t *testing.T) {
		expectUsagePanic(t, shadowApp(Flag{Name: "page", Kind: String, Usage: "page size"}),
			[]string{"wf", "list"},
			`command "wf list" flag "page" has kind string, but the same-named flag from command "wf" has kind integer`)
	})

	t.Run("a same-kind shadow parses the leaf's value", func(t *testing.T) {
		inv, err := Parse(shadowApp(Flag{Name: "page", Kind: Int, Usage: "page size"}), []string{"wf", "list", "--page", "5"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Ints["page"] != 5 {
			t.Errorf("page = %d, want 5 from the leaf's own --page", inv.Ints["page"])
		}
	})
}

// TestValidateRejectsDuplicateFlagNames pins that a flag name repeated in one
// registered list, or on a command and in the common flags, is a caller error
// rather than a pflag panic at registration.
func TestValidateRejectsDuplicateFlagNames(t *testing.T) {
	t.Run("the same name twice in one list", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Flags: []Flag{
					{Name: "depth", Kind: Int, Usage: "fetch depth"},
					{Name: "depth", Kind: Int, Usage: "fetch depth"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, []string{"probe", "u"}, `command "probe" declares flag "depth" twice`)
	})

	t.Run("a common flag name repeated on a leaf", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			CommonFlags: []Flag{{Name: "format", Kind: String, Usage: "output format"}},
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Flags:  []Flag{{Name: "format", Kind: String, Usage: "output format"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, []string{"probe", "u"}, `command "probe" flag "format" repeats a common flag name`)
	})
}

// TestValidateRejectsHelpShorthand pins the caller error for a flag that
// claims the help shorthand: cobra's default help flag reserves "h", so a
// declaration using it would panic at execute instead of failing at
// startup.
func TestValidateRejectsHelpShorthand(t *testing.T) {
	app := testApp()
	app.Commands[0].Flags = append(app.Commands[0].Flags,
		Flag{Name: "hint", Kind: Bool, Short: "h", Usage: "print a hint"})
	expectUsagePanic(t, app, []string{"reconcile", "u"}, `flag "hint" uses shorthand "h"`)
}

// TestValidateRejectsReservedShorthandOnPersistentFlags pins that the
// reserved-shorthand guard covers every node's persistent flags: they
// inherit into the subcommands' flag sets, where pflag would otherwise
// panic during execute.
func TestValidateRejectsReservedShorthandOnPersistentFlags(t *testing.T) {
	t.Run("a top-level parent", func(t *testing.T) {
		app := &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow parent",
				PersistentFlags: []Flag{{Name: "history", Kind: Bool, Short: "h", Usage: "keep history"}},
				Subcommands: []*Command{{
					Name: "list", Short: "list workflows",
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
						return map[string]any{"status": "ok"}, nil
					},
				}},
			}},
		}
		expectUsagePanic(t, app, []string{"wf", "list"}, `flag "history" uses shorthand "h"`)
	})

	t.Run("an inner nested node", func(t *testing.T) {
		app := &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow parent",
				Subcommands: []*Command{{
					Name: "ctl", Short: "controller group",
					PersistentFlags: []Flag{{Name: "hint", Kind: Bool, Short: "h", Usage: "print a hint"}},
					Subcommands: []*Command{{
						Name: "enable", Short: "enable one controller",
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{"status": "ok"}, nil
						},
					}},
				}},
			}},
		}
		expectUsagePanic(t, app, []string{"wf", "ctl", "enable"}, `command "wf ctl" flag "hint" uses shorthand "h"`)
	})
}

// TestHelpShorthandFollowsRegisteredFlags pins that the reserved "h" follows
// the flag set the builder registers: a nested leaf's own flag cannot claim it
// without a help flag, while an ancestor's persistent help flag frees it.
func TestHelpShorthandFollowsRegisteredFlags(t *testing.T) {
	t.Run("a nested leaf's own flag without a help flag", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow group",
				Subcommands: []*Command{{
					Name: "list", Short: "list workflows",
					Flags:  []Flag{{Name: "hint", Kind: Bool, Short: "h", Usage: "print a hint"}},
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
				}},
			}},
		}, []string{"wf", "list"}, `command "wf list" flag "hint" uses shorthand "h", reserved for the help flag`)
	})

	t.Run("a help flag on an ancestor's persistent flags", func(t *testing.T) {
		app := &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow group",
				PersistentFlags: []Flag{{Name: "help", Kind: Bool, Usage: "show help"}},
				Subcommands: []*Command{{
					Name: "list", Short: "list workflows",
					Flags: []Flag{{Name: "host", Kind: String, Short: "h", Usage: "host to talk to"}},
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
						return map[string]any{"host": inv.Flags["host"]}, nil
					},
				}},
			}},
		}
		// Parse panics on an invalid declaration, so reaching the assertions
		// proves the "h" shorthand passed validation.
		inv, err := Parse(app, []string{"wf", "list", "-h", "example"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["host"] != "example" {
			t.Errorf("host = %q, want example: -h is the leaf flag's shorthand", inv.Flags["host"])
		}
	})
}

// TestIgnoredFlagListsAreNotValidated pins that a list the builder never
// registers is neither checked nor allowed to reserve anything: a leaf's
// persistent flags and an inner node's plain flags neither collide with an
// inherited shorthand nor declare the help flag.
func TestIgnoredFlagListsAreNotValidated(t *testing.T) {
	t.Run("a leaf's persistent flags collide with an inherited shorthand", func(t *testing.T) {
		app := &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow group",
				PersistentFlags: []Flag{{Name: "page", Kind: Int, Short: "p", Usage: "page size"}},
				Subcommands: []*Command{{
					Name: "list", Short: "list workflows",
					PersistentFlags: []Flag{{Name: "pretty", Kind: Bool, Short: "p", Usage: "print pretty"}},
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
						return map[string]any{"page": inv.Ints["page"]}, nil
					},
				}},
			}},
		}
		if code, stdout, stderr := runApp(t, app, &testAdapter{}, "wf", "list", "--page", "2"); code != 0 {
			t.Errorf("exit = %d, stderr = %s; want a clean run", code, stderr)
		} else if page := jsonField(t, stdout)["page"]; page != float64(2) {
			t.Errorf("page = %v, want the inherited flag's 2", page)
		}
	})

	t.Run("an inner node's plain flags collide with an inherited shorthand", func(t *testing.T) {
		app := &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow group",
				PersistentFlags: []Flag{{Name: "page", Kind: Int, Short: "p", Usage: "page size"}},
				Subcommands: []*Command{{
					Name: "ctl", Short: "controller group",
					Flags: []Flag{{Name: "pretty", Kind: Bool, Short: "p", Usage: "print pretty"}},
					Subcommands: []*Command{{
						Name: "list", Short: "list controllers",
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{"page": inv.Ints["page"]}, nil
						},
					}},
				}},
			}},
		}
		if code, stdout, stderr := runApp(t, app, &testAdapter{}, "wf", "--page", "2", "ctl", "list"); code != 0 {
			t.Errorf("exit = %d, stderr = %s; want a clean run", code, stderr)
		} else if page := jsonField(t, stdout)["page"]; page != float64(2) {
			t.Errorf("page = %v, want the inherited flag's 2", page)
		}
	})

	t.Run("a leaf's persistent help flag declares nothing", func(t *testing.T) {
		expectUsagePanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				PersistentFlags: []Flag{{Name: "help", Kind: Bool, Usage: "show help"}},
				Flags:           []Flag{{Name: "hint", Kind: Bool, Short: "h", Usage: "print a hint"}},
				Handle:          func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, []string{"probe"}, `command "probe" flag "hint" uses shorthand "h", reserved for the help flag`)
	})
}

// TestDeclaredHelpFlag pins that an app's own "help" flag keeps working:
// cobra defers to it and skips its own help flag, so the declaration passes
// validation and -h parses as the app's flag.
func TestDeclaredHelpFlag(t *testing.T) {
	app := &App{
		Name: "t", Version: "v1",
		Commands: []*Command{{
			Name:  "probe",
			Short: "run one probe",
			Flags: []Flag{{Name: "help", Kind: Bool, Short: "h", Usage: "show help"}},
			Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
				return map[string]any{"command": "probe", "status": "ok"}, nil
			},
		}},
	}
	for _, argv := range [][]string{{"probe", "-h"}, {"probe", "--help"}, {"probe", "-help"}} {
		code, stdout, stderr := runApp(t, app, &testAdapter{}, argv...)
		if code != 0 {
			t.Errorf("%v: exit = %d, want 0 (stderr: %s)", argv, code, stderr)
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
			t.Errorf("%v: stdout = %q, want plain help text", argv, stdout)
		}
	}

	// A normal dispatch is unaffected by the declared help flag.
	code, stdout, _ := runApp(t, app, &testAdapter{}, "probe")
	if code != 0 {
		t.Fatalf("probe: exit = %d, want 0", code)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("probe: stdout is not one JSON value: %v (%q)", err, stdout)
	}
	if envelope["command"] != "probe" {
		t.Errorf("envelope = %v, want the dispatched probe", envelope)
	}
}

// TestDeclaredHelpFlagFreesTheHShorthand pins that a merged flag set which
// already declares a flag named "help" leaves "h" to the app: cobra's
// InitDefaultHelpFlag then never claims it, so another flag may use it.
func TestDeclaredHelpFlagFreesTheHShorthand(t *testing.T) {
	app := &App{
		Name:        "t",
		Version:     "v1",
		CommonFlags: []Flag{{Name: "help", Kind: Bool, Usage: "show help"}},
		Commands: []*Command{{
			Name:  "probe",
			Short: "run one probe",
			Flags: []Flag{{Name: "host", Kind: String, Short: "h", Usage: "host to talk to"}},
			Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
				return map[string]any{"command": "probe", "host": inv.Flags["host"]}, nil
			},
		}},
	}
	// Parse panics on an invalid declaration, so reaching the assertions
	// proves the "h" shorthand passed validation.
	inv, err := Parse(app, []string{"probe", "-h", "example"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Flags["host"] != "example" {
		t.Errorf("host = %q, want example: -h is the app's shorthand", inv.Flags["host"])
	}
}

// TestParseValidatesLikeRun pins that Parse reports invalid declarations
// with the same validation failure Run reports, instead of failing later
// in the parse with a different message.
func TestParseValidatesLikeRun(t *testing.T) {
	mk := func() *App {
		app := testApp()
		app.Commands[0].Flags = append(app.Commands[0].Flags,
			Flag{Name: "hint", Kind: Bool, Short: "h", Usage: "print a hint"})
		return app
	}

	var runMsg string
	func() {
		defer func() { runMsg, _ = recover().(string) }()
		Run(mk(), &testAdapter{}, []string{"reconcile", "u"}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	if runMsg == "" {
		t.Fatal("Run did not panic on the invalid declaration")
	}
	if !strings.Contains(runMsg, `flag "hint" uses shorthand "h"`) {
		t.Errorf("Run panic = %q, want the library's validation message", runMsg)
	}

	var parseMsg string
	func() {
		defer func() { parseMsg, _ = recover().(string) }()
		Parse(mk(), []string{"reconcile", "u"})
	}()
	if parseMsg != runMsg {
		t.Errorf("Parse panic = %q, want Run's %q", parseMsg, runMsg)
	}
}

// TestNilArgvIsConsistentAcrossEntryPoints pins that nil argv means the
// process's own arguments on both entry points, and that a dispatch
// error's envelope labels the resolved command.
func TestNilArgvIsConsistentAcrossEntryPoints(t *testing.T) {
	orig := os.Args
	defer func() { os.Args = orig }()
	os.Args = []string{"testcli", "reconcile", "u"}

	inv, err := Parse(testApp(), nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if inv.Command != "reconcile" || len(inv.Args) != 1 || inv.Args[0] != "u" {
		t.Errorf("Parse(nil) = %q %v, want the process's own arguments", inv.Command, inv.Args)
	}

	os.Args = []string{"testcli", "bogus"}
	var stdout, stderr bytes.Buffer
	code := Run(testApp(), &testAdapter{}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run(nil) over an unknown command: exit = %d, want 2", code)
	}
	var env map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not one JSON value: %v (%q)", err, stdout.String())
	}
	if env["command"] != "bogus" {
		t.Errorf("envelope command = %v, want bogus: the resolved command, not an empty label", env["command"])
	}
}

// TestRunNilArgvFallsBackToOsArgs pins the nil-argv convention through the
// retry: Run hands cobra nil argv, cobra falls back to os.Args[1:], and a
// single-dash long flag typed on the real command line still rewrites.
func TestRunNilArgvFallsBackToOsArgs(t *testing.T) {
	orig := os.Args
	defer func() { os.Args = orig }()

	var nilOut, explicitOut bytes.Buffer
	os.Args = []string{"testcli", "reconcile", "-head", "abc", "u"}
	nilCode := Run(testApp(), &testAdapter{}, nil, &nilOut, io.Discard)
	os.Args = []string{"testcli", "reconcile", "--head", "abc", "u"}
	explicitCode := Run(testApp(), &testAdapter{}, os.Args[1:], &explicitOut, io.Discard)
	if nilCode != 0 || explicitCode != 0 {
		t.Fatalf("exit codes = %d and %d, want 0 and 0", nilCode, explicitCode)
	}
	if nilOut.String() != explicitOut.String() {
		t.Errorf("nil argv stdout = %q, want the explicit run's %q", nilOut.String(), explicitOut.String())
	}
}

// TestNestedNoInterspersed pins nested NoInterspersed leaves: a nested
// command declaring NoInterspersed keeps flag-looking tokens after its
// first positional as positionals, while flags before it still parse.
func TestNestedNoInterspersed(t *testing.T) {
	app := &App{
		Name:    "ncli",
		Version: "vtest",
		Commands: []*Command{{
			Name:  "wf",
			Short: "workflow utilities",
			Subcommands: []*Command{{
				Name:           "scan",
				Short:          "scan fixed arguments",
				NoInterspersed: true,
				Positionals:    []Positional{{Name: "args", Required: true, Variadic: true}},
				Flags:          []Flag{{Name: "agent", Kind: String, Usage: "agent name"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{"args": inv.Args, "agent": inv.Flags["agent"]}, nil
				},
			}},
		}},
	}
	inv, err := Parse(app, []string{"wf", "scan", "tgt", "-agent", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Args) != 3 || inv.Args[1] != "-agent" || inv.Args[2] != "x" {
		t.Errorf("args = %v, want [tgt -agent x]", inv.Args)
	}
	if inv.Flags["agent"] != "" {
		t.Errorf("agent = %q, want empty: the token is positional", inv.Flags["agent"])
	}
	inv, err = Parse(app, []string{"wf", "scan", "-agent", "x", "tgt"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Flags["agent"] != "x" || len(inv.Args) != 1 || inv.Args[0] != "tgt" {
		t.Errorf("agent = %q, args = %v; want x and [tgt]", inv.Flags["agent"], inv.Args)
	}
}

// TestDeepNestingValidationPath pins that depth-3 validation diagnostics
// name the full command path, not the last two names alone.
func TestDeepNestingValidationPath(t *testing.T) {
	app := &App{
		Name:    "t",
		Version: "v1",
		Commands: []*Command{{
			Name:  "wf",
			Short: "workflow root",
			Subcommands: []*Command{{
				Name:  "ctl",
				Short: "controller group",
				Subcommands: []*Command{{
					Name:  "enable",
					Short: "enable one controller",
				}},
			}},
		}},
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("validate did not reject a depth-3 leaf without a handler")
		}
		if !strings.Contains(r.(string), `command "wf ctl enable" must declare exactly one of Handle, Raw, or Stream`) {
			t.Errorf("panic = %v, want the full command path in the diagnostic", r)
		}
	}()
	Run(app, &testAdapter{}, []string{"wf", "ctl", "enable"}, &bytes.Buffer{}, &bytes.Buffer{})
}

// TestLeafRejectsMultipleHandlers pins that a leaf declaring more than one of
// Handle, Raw, and Stream fails at startup instead of dispatching one silently.
func TestLeafRejectsMultipleHandlers(t *testing.T) {
	handle := func(context.Context, Invocation, io.Writer) (any, error) { return nil, nil }
	raw := func(context.Context, Invocation, io.Writer) error { return nil }
	stream := func(context.Context, Invocation, io.Writer, io.Writer) (any, error) { return nil, nil }
	for _, tc := range []struct {
		name string
		cmd  *Command
	}{
		{"Handle and Stream", &Command{Name: "watch", Short: "watch one target", Handle: handle, Stream: stream}},
		{"Handle and Raw", &Command{Name: "watch", Short: "watch one target", Handle: handle, Raw: raw}},
		{"Raw and Stream", &Command{Name: "watch", Short: "watch one target", Raw: raw, Stream: stream}},
		{"all three", &Command{Name: "watch", Short: "watch one target", Handle: handle, Raw: raw, Stream: stream}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{Name: "t", Version: "v1", Commands: []*Command{tc.cmd}}
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("validate did not reject a leaf with two handlers")
				}
				if !strings.Contains(r.(string), `command "watch" must declare exactly one of Handle, Raw, or Stream`) {
					t.Errorf("panic = %v, want the multi-handler diagnostic", r)
				}
			}()
			Run(app, &testAdapter{}, []string{"watch"}, &bytes.Buffer{}, &bytes.Buffer{})
		})
	}
}
