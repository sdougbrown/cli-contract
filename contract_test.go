package clicontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
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
				Name:  "reconcile",
				Short: "collect feedback and establish the baseline",
				URL:   true,
				Flags: []Flag{
					{Name: "head", Kind: String, Usage: "expected head SHA"},
				},
				Handle: func(ctx context.Context, inv Invocation) (any, error) {
					return map[string]any{"command": "reconcile", "status": "updated", "url": inv.URL, "head": inv.Flags["head"]}, nil
				},
			},
			{
				Name:  "wait",
				Short: "block until new events arrive",
				URL:   true,
				Flags: []Flag{
					{Name: "consumer", Kind: String, Usage: "name identifying this reader"},
					{Name: "timeout", Kind: String, Usage: "maximum wait duration", Default: "30m"},
					{Name: "exit-on-events", Kind: Bool, Usage: "exit 3 on events"},
				},
				Handle: func(ctx context.Context, inv Invocation) (any, error) {
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
				Name:  "ack",
				Short: "acknowledge delivered event IDs",
				URL:   true,
				Flags: []Flag{
					{Name: "event", Kind: Repeat, Usage: "event ID to acknowledge"},
				},
				Handle: func(ctx context.Context, inv Invocation) (any, error) {
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
						Handle: func(ctx context.Context, inv Invocation) (any, error) {
							return map[string]any{"command": "store", "status": "ok"}, nil
						},
					},
					{
						Name:  "reset",
						Short: "delete the store",
						Flags: []Flag{{Name: "target", Kind: String, Usage: "scope the reset to one target URL"}},
						Handle: func(ctx context.Context, inv Invocation) (any, error) {
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

func TestRunVersionFlag(t *testing.T) {
	adapter := &testAdapter{}
	code, stdout, _ := runApp(t, testApp(), adapter, "--version")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.HasPrefix(stdout, "testcli vtest\n") {
		t.Errorf("version output = %q", stdout)
	}
}

func TestRunSignals(t *testing.T) {
	app := testApp()
	app.Commands[0].Handle = func(ctx context.Context, inv Invocation) (any, error) {
		return map[string]any{"command": "reconcile", "status": "updated"}, nil
	}
	for _, tc := range []struct {
		name string
		sig  os.Signal
		code int
	}{
		{"SIGINT", os.Interrupt, 130},
		{"SIGTERM", syscall.SIGTERM, 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &testAdapter{seams: &testSeams{signal: tc.sig}}
			code, _, _ := runApp(t, app, adapter, "reconcile", "u")
			if code != tc.code {
				t.Fatalf("exit code = %d, want %d", code, tc.code)
			}
		})
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
		if inv.Command != "ack" || inv.URL != "u" || len(inv.Events) != 2 {
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

	t.Run("raw commands produce no invocation", func(t *testing.T) {
		if _, err := Parse(testApp(), []string{"schema"}); err == nil {
			t.Error("schema: err = nil, want a caller error")
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

func TestValidatePanicsOnDrift(t *testing.T) {
	app := testApp()
	app.Commands = append(app.Commands, &Command{Name: "bogus", Handle: func(ctx context.Context, inv Invocation) (any, error) {
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
