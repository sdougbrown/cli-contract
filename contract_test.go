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
	"syscall"
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
	app.Commands[0].Handle = func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
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

func TestHandlerOwnedSignals(t *testing.T) {
	app := testApp()
	app.HandlerOwnedSignals = true
	app.Commands[0].Handle = func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
		// A server-style handler: it manages signals itself and returns
		// its own exit decision.
		return map[string]any{"command": "reconcile", "status": "updated"}, nil
	}
	adapter := &testAdapter{seams: &testSeams{signal: os.Interrupt}}
	code, _, _ := runApp(t, app, adapter, "reconcile", "u")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: the handler owns signals", code)
	}
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

// typedFlagApp declares Int, Duration, bool shorthands, String, a parent
// persistent flag, and a NoInterspersed command: the kinds and modes the
// single-dash normalization has to respect.
func typedFlagApp() *App {
	return &App{
		Name:        "toolcli",
		Version:     "vtest",
		RootShort:   "toolcli exercises typed flags",
		CommonFlags: []Flag{{Name: "format", Kind: String, Usage: "output format"}},
		Commands: []*Command{
			{
				Name:  "probe",
				Short: "run one probe",
				Positionals: []Positional{
					{Name: "target", Required: true},
					{Name: "extra", Variadic: true},
				},
				Flags: []Flag{
					{Name: "retries", Kind: Int, Short: "r", Usage: "probe retries"},
					{Name: "wait", Kind: Duration, Short: "w", Usage: "how long to wait"},
					{Name: "verbose", Kind: Bool, Short: "v", Usage: "log verbosely"},
					{Name: "brief", Kind: Bool, Short: "b", Usage: "log briefly"},
					{Name: "tag", Kind: Repeat, Short: "t", Usage: "record tags"},
					{Name: "label", Kind: String, Short: "l", Usage: "probe label"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return map[string]any{
						"retries": inv.Ints["retries"],
						"wait":    inv.Durations["wait"].String(),
						"verbose": inv.Bools["verbose"],
						"brief":   inv.Bools["brief"],
						"label":   inv.Flags["label"],
						"format":  inv.Flags["format"],
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
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
							return map[string]any{"region": inv.Flags["region"]}, nil
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
					return map[string]any{"args": inv.Args, "agent": inv.Flags["agent"]}, nil
				},
			},
		},
	}
}

func TestSingleDashLongFlags(t *testing.T) {
	t.Run("string flag with separate and inline values", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-label", "x", "-label=y", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["label"] != "y" {
			t.Errorf("label = %q, want y", inv.Flags["label"])
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

	t.Run("persistent flag via single dash on a nested subcommand", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"remote", "list", "-region", "eu"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["region"] != "eu" {
			t.Errorf("region = %q, want eu", inv.Flags["region"])
		}
	})

	t.Run("shorthand cluster that is not a long name still parses as shorthands", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-vb", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if !inv.Bools["verbose"] || !inv.Bools["brief"] {
			t.Errorf("verbose = %v, brief = %v, want both true", inv.Bools["verbose"], inv.Bools["brief"])
		}
	})

	t.Run("a flag-looking value after a value-taking flag stays the value", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-label", "-verbose", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["label"] != "-verbose" {
			t.Errorf("label = %q, want -verbose", inv.Flags["label"])
		}
		if inv.Bools["verbose"] {
			t.Error("verbose = true, want false: the token was -label's value")
		}
	})

	t.Run("a lone value-taking shorthand absorbs the next token", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-l", "-verbose", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["label"] != "-verbose" {
			t.Errorf("label = %q, want -verbose: the token is -l's value", inv.Flags["label"])
		}
		if inv.Bools["verbose"] {
			t.Error("verbose = true, want false: the token was -l's value")
		}
	})

	t.Run("a cluster ending in a value-taking shorthand absorbs the next token", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-vl", "name", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if !inv.Bools["verbose"] || inv.Flags["label"] != "name" {
			t.Errorf("verbose = %v, label = %q; want true and name", inv.Bools["verbose"], inv.Flags["label"])
		}
	})

	t.Run("an inherited persistent flag before the command names", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"-region", "eu", "remote", "list"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["region"] != "eu" {
			t.Errorf("region = %q, want eu: the flag is inherited by the resolved command", inv.Flags["region"])
		}
	})

	t.Run("value-taking shorthands of every kind", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "-r", "3", "-w=2s", "-t", "a", "-t=b", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Ints["retries"] != 3 || inv.Durations["wait"] != 2*time.Second {
			t.Errorf("retries = %d, wait = %v; want 3 and 2s", inv.Ints["retries"], inv.Durations["wait"])
		}
		if len(inv.Events) != 2 || inv.Events[0] != "a" || inv.Events[1] != "b" {
			t.Errorf("events = %v, want [a b]", inv.Events)
		}
	})

	t.Run("tokens after a bare double dash are untouched", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"probe", "tgt", "--", "-label"})
		if err != nil {
			t.Fatal(err)
		}
		if len(inv.Args) != 2 || inv.Args[1] != "-label" {
			t.Errorf("args = %v, want [tgt -label]", inv.Args)
		}
		if inv.Flags["label"] != "" {
			t.Errorf("label = %q, want empty: the token is positional", inv.Flags["label"])
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

	t.Run("a parent persistent flag before the nested name", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"remote", "-region", "eu", "list"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["region"] != "eu" {
			t.Errorf("region = %q, want eu", inv.Flags["region"])
		}
	})

	t.Run("an undeclared single-dash token fails as a caller error", func(t *testing.T) {
		_, err := Parse(typedFlagApp(), []string{"probe", "-bogus", "tgt"})
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Fatalf("err = %v, want a UsageError", err)
		}
	})

	t.Run("NoInterspersed keeps dashes after the first positional", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"scan", "tgt", "-agent", "x"})
		if err != nil {
			t.Fatal(err)
		}
		if len(inv.Args) != 3 || inv.Args[1] != "-agent" || inv.Args[2] != "x" {
			t.Errorf("args = %v, want [tgt -agent x]", inv.Args)
		}
		if inv.Flags["agent"] != "" {
			t.Errorf("agent = %q, want empty: the token is positional", inv.Flags["agent"])
		}
	})

	t.Run("a value token spelling another flag stays the value across levels", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"-format", "json", "probe", "tgt", "-label", "-format"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["format"] != "json" {
			t.Errorf("format = %q, want json", inv.Flags["format"])
		}
		if inv.Flags["label"] != "-format" {
			t.Errorf("label = %q, want -format: the token was -label's value", inv.Flags["label"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})

	t.Run("NoInterspersed passthrough after a common flag keeps tokens byte-identical", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"-format", "json", "scan", "tgt", "-format"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["format"] != "json" {
			t.Errorf("format = %q, want json", inv.Flags["format"])
		}
		want := []string{"tgt", "-format"}
		if !slices.Equal(inv.Args, want) {
			t.Errorf("args = %v, want %v: passthrough tokens must stay byte-identical", inv.Args, want)
		}
	})

	t.Run("flags before and after the subcommand name resolve per level", func(t *testing.T) {
		inv, err := Parse(typedFlagApp(), []string{"-format", "json", "probe", "-label", "x", "tgt"})
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flags["format"] != "json" || inv.Flags["label"] != "x" {
			t.Errorf("format = %q, label = %q; want json and x", inv.Flags["format"], inv.Flags["label"])
		}
		if len(inv.Args) != 1 || inv.Args[0] != "tgt" {
			t.Errorf("args = %v, want [tgt]", inv.Args)
		}
	})
}

func TestSingleDashLongFlagsOnRun(t *testing.T) {
	adapter := &testAdapter{}
	code, stdout, stderr := runApp(t, typedFlagApp(), adapter, "probe", "-retries", "3", "-wait=2s", "-verbose", "tgt")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	env := jsonField(t, stdout)
	if env["retries"] != float64(3) || env["wait"] != "2s" || env["verbose"] != true {
		t.Errorf("env = %v, want retries 3, wait 2s, verbose true", env)
	}

	// -help on a subcommand rewrites to the declared help flag: the
	// plain-text help path exits 0 without an envelope.
	code, stdout, _ = runApp(t, typedFlagApp(), &testAdapter{}, "probe", "-help")
	if code != 0 {
		t.Errorf("exit = %d, want 0 for the probe -help help path", code)
	}
	if strings.HasPrefix(stdout, "{") {
		t.Errorf("stdout = %q, want plain help text", stdout)
	}

	// The help and version flags are never registered early: while they
	// are undeclared, cobra's stripFlags reads --version/-h as consuming
	// the next token, so resolution stops at the root and the root prints
	// the version or the root help instead of failing on the subcommand.
	code, stdout, stderr = runApp(t, typedFlagApp(), &testAdapter{}, "--version", "probe")
	if code != 0 || stdout != "toolcli vtest\n" {
		t.Errorf("--version probe: exit = %d, stdout = %q, stderr = %q; want version and exit 0", code, stdout, stderr)
	}
	code, stdout, _ = runApp(t, typedFlagApp(), &testAdapter{}, "-version")
	if code != 0 || stdout != "toolcli vtest\n" {
		t.Errorf("-version: exit = %d, stdout = %q; want version and exit 0", code, stdout)
	}

	// A single-dash -version before a subcommand name still reaches the
	// root version flag: after the rewrite, cobra's stripFlags consumes
	// the token after the double-dash flag, so resolution stops at the
	// root and the version prints instead of a caller error on probe.
	code, stdout, _ = runApp(t, typedFlagApp(), &testAdapter{}, "-version", "probe")
	if code != 0 || stdout != "toolcli vtest\n" {
		t.Errorf("-version probe: exit = %d, stdout = %q; want version and exit 0", code, stdout)
	}
	var rootHelp string
	_, rootHelp, _ = runApp(t, typedFlagApp(), &testAdapter{}, "--help")
	code, stdout, _ = runApp(t, typedFlagApp(), &testAdapter{}, "-help")
	if code != 0 || stdout != rootHelp {
		t.Errorf("-help: exit = %d; want the root help and exit 0", code)
	}
	code, stdout, _ = runApp(t, typedFlagApp(), &testAdapter{}, "-h", "probe")
	if code != 0 || stdout != rootHelp {
		t.Errorf("-h probe: exit = %d; want the root help and exit 0, not the subcommand help", code)
	}
}

// TestSingleDashLongNameBeatsShorthandCluster pins the precedence between a
// long name and bool shorthands: a single-dash token that spells a declared
// long flag is read as that flag, even when its letters are also bool
// shorthands — the long name wins, as the standard flag package reads it.
func TestSingleDashLongNameBeatsShorthandCluster(t *testing.T) {
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
			},
			Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
				return map[string]any{
					"verbose": inv.Bools["verbose"],
					"brief":   inv.Bools["brief"],
					"vb":      inv.Flags["vb"],
				}, nil
			},
		}},
	}
	inv, err := Parse(app, []string{"run", "-vb", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Flags["vb"] != "x" {
		t.Errorf("vb = %q, want x: the long name wins over the shorthand cluster", inv.Flags["vb"])
	}
	if inv.Bools["verbose"] || inv.Bools["brief"] {
		t.Errorf("verbose = %v, brief = %v; want both false: the token was the long flag", inv.Bools["verbose"], inv.Bools["brief"])
	}
}

// TestNestedNoInterspersed pins nested NoInterspersed leaves: a nested
// command declaring NoInterspersed keeps flag-looking tokens after its first
// positional as positionals, while flags before it still parse.
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

// TestValidateRejectsHelpShorthand pins the caller error for a flag that
// claims the help shorthand: cobra's default help flag reserves "h", so a
// declaration using it would panic at execute instead of failing at startup.
func TestValidateRejectsHelpShorthand(t *testing.T) {
	app := testApp()
	app.Commands[0].Flags = append(app.Commands[0].Flags,
		Flag{Name: "hint", Kind: Bool, Short: "h", Usage: "print a hint"})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("validate did not reject a flag with the help shorthand")
		}
		if !strings.Contains(r.(string), `flag "hint" uses shorthand "h"`) {
			t.Errorf("panic = %v", r)
		}
	}()
	Run(app, &testAdapter{}, []string{"reconcile", "u"}, &bytes.Buffer{}, &bytes.Buffer{})
}

// TestSuppliedFlags pins Invocation.Supplied: the names of flags given on
// the command line, covering every kind and inherited persistent and
// common flags. A defaulted flag that was not typed keeps its default value
// and stays absent from the map, so an explicit zero is distinguishable from
// absent.
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

	// An explicitly-passed empty value is Supplied with its empty value.
	inv, err := Parse(app, []string{"job", "--name", ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["name"]; !ok {
		t.Error("Supplied[name] is absent, want present: the flag was typed")
	}
	if inv.Flags["name"] != "" {
		t.Errorf("Flags[name] = %q, want the explicit empty value", inv.Flags["name"])
	}

	// An omitted flag with a declared default keeps its value and stays
	// absent from Supplied.
	inv, err = Parse(app, []string{"job"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["retries"]; ok {
		t.Error("Supplied[retries] is present, want absent: the flag was not typed")
	}
	if inv.Ints["retries"] != 16 {
		t.Errorf("retries = %d, want the declared default 16", inv.Ints["retries"])
	}

	// A bool flag is Supplied given as its long name.
	inv, err = Parse(app, []string{"job", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["verbose"]; !ok {
		t.Error("Supplied[verbose] is absent, want present for --verbose")
	}
	if !inv.Bools["verbose"] {
		t.Error("verbose = false, want true")
	}

	// And given as its shorthand.
	inv, err = Parse(app, []string{"job", "-v"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["verbose"]; !ok {
		t.Error("Supplied[verbose] is absent, want present for -v")
	}

	// A repeat flag is Supplied with every value collected.
	inv, err = Parse(app, []string{"job", "--tag", "a", "--tag=b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["tag"]; !ok {
		t.Error("Supplied[tag] is absent, want present")
	}
	if len(inv.Events) != 2 || inv.Events[0] != "a" || inv.Events[1] != "b" {
		t.Errorf("events = %v, want [a b]", inv.Events)
	}

	// A common flag given before the subcommand is Supplied.
	inv, err = Parse(app, []string{"-format", "json", "job"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["format"]; !ok {
		t.Error("Supplied[format] is absent, want present")
	}
	if inv.Flags["format"] != "json" {
		t.Errorf("format = %q, want json", inv.Flags["format"])
	}

	// A Duration flag is Supplied when typed.
	inv, err = Parse(app, []string{"job", "--wait", "3s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["wait"]; !ok {
		t.Error("Supplied[wait] is absent, want present for --wait")
	}
	if inv.Durations["wait"] != 3*time.Second {
		t.Errorf("wait = %v, want 3s", inv.Durations["wait"])
	}

	// A repeated bool is Supplied once, with the last value.
	inv, err = Parse(app, []string{"job", "--verbose", "--verbose=false"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["verbose"]; !ok {
		t.Error("Supplied[verbose] is absent, want present")
	}
	if inv.Bools["verbose"] {
		t.Error("verbose = true, want false: the last value wins")
	}

	// The handler sees the same Supplied as Parse for the same argv.
	argv := []string{"-format", "json", "job", "-v", "--name", "x", "--tag", "t1", "--tag=t2"}
	if code, _, _ := runApp(t, app, &testAdapter{}, argv...); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	parsed, err := Parse(app, argv)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(handlerInv.Supplied, parsed.Supplied) {
		t.Errorf("handler Supplied = %v, want Parse's %v", handlerInv.Supplied, parsed.Supplied)
	}

	// A persistent flag given before a nested name is Supplied on the
	// leaf's Invocation.
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
	inv, err = Parse(nested, []string{"wf", "-region", "eu", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Supplied["region"]; !ok {
		t.Error("Supplied[region] is absent, want present")
	}
	if inv.Flags["region"] != "eu" {
		t.Errorf("region = %q, want eu", inv.Flags["region"])
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

// TestValidateRejectsReservedShorthandOnPersistentFlags pins that the
// reserved-shorthand guard covers every node's persistent flags: they
// inherit into the subcommands' flag sets, where pflag would otherwise
// panic during execute.
func TestValidateRejectsReservedShorthandOnPersistentFlags(t *testing.T) {
	t.Run("a top-level parent", func(t *testing.T) {
		app := &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name:  "wf",
				Short: "workflow parent",
				PersistentFlags: []Flag{{Name: "history", Kind: Bool, Short: "h", Usage: "keep history"}},
				Subcommands: []*Command{{
					Name:  "list",
					Short: "list workflows",
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
						return map[string]any{"status": "ok"}, nil
					},
				}},
			}},
		}
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("validate did not reject a reserved shorthand on a parent's persistent flags")
			}
			if !strings.Contains(r.(string), `flag "history" uses shorthand "h"`) {
				t.Errorf("panic = %v, want the library's validation message", r)
			}
		}()
		Run(app, &testAdapter{}, []string{"wf", "list"}, &bytes.Buffer{}, &bytes.Buffer{})
	})

	t.Run("an inner nested node", func(t *testing.T) {
		app := &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name:  "wf",
				Short: "workflow parent",
				Subcommands: []*Command{{
					Name:  "ctl",
					Short: "controller group",
					PersistentFlags: []Flag{{Name: "hint", Kind: Bool, Short: "h", Usage: "print a hint"}},
					Subcommands: []*Command{{
						Name:  "enable",
						Short: "enable one controller",
						Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
								return map[string]any{"status": "ok"}, nil
						},
					}},
				}},
			}},
		}
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("validate did not reject a reserved shorthand on an inner node's persistent flags")
			}
			if !strings.Contains(r.(string), `command "wf ctl" flag "hint" uses shorthand "h"`) {
				t.Errorf("panic = %v, want the library's validation message", r)
			}
		}()
		Run(app, &testAdapter{}, []string{"wf", "ctl", "enable"}, &bytes.Buffer{}, &bytes.Buffer{})
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

// TestRunNilArgvFallsBackToOsArgs pins the nil-argv convention: Run passes
// os.Args[1:] through normalization, so single-dash long flags work on the
// real command line too.
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

// TestValidateRejectsShorthandCollisions pins the shorthand validation:
// a multi-character shorthand, and two flags that share a shorthand across
// a command's effective surface, fail at startup as UsageErrors instead of
// panicking inside pflag at registration. A flag that shadows a same-named
// one keeps its shorthand.
func TestValidateRejectsShorthandCollisions(t *testing.T) {
	expectPanic := func(t *testing.T, app *App, want string) {
		t.Helper()
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("validate did not reject the declaration")
			}
			if !strings.Contains(r.(string), want) {
				t.Errorf("panic = %v, want it to contain %q", r, want)
			}
		}()
		Run(app, &testAdapter{}, []string{"probe", "u"}, &bytes.Buffer{}, &bytes.Buffer{})
	}

	t.Run("two flags of one command sharing a shorthand", func(t *testing.T) {
		expectPanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Flags: []Flag{
					{Name: "verbose", Kind: Bool, Short: "v", Usage: "log verbosely"},
					{Name: "vanilla", Kind: Bool, Short: "v", Usage: "log vanilla"},
				},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, `flags "verbose" and "vanilla" share the "v" shorthand`)
	})

	t.Run("a common flag and a command flag sharing a shorthand", func(t *testing.T) {
		expectPanic(t, &App{
			Name: "t", Version: "v1",
			CommonFlags: []Flag{{Name: "format", Kind: String, Short: "f", Usage: "output format"}},
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Flags: []Flag{{Name: "filter", Kind: String, Short: "f", Usage: "filter expression"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, `flags "format" and "filter" share the "f" shorthand`)
	})

	t.Run("an inherited persistent flag and a leaf flag sharing a shorthand", func(t *testing.T) {
		expectPanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "wf", Short: "workflow utilities",
				PersistentFlags: []Flag{{Name: "page", Kind: Int, Short: "p", Usage: "page size"}},
				Subcommands: []*Command{{
					Name: "list", Short: "list workflows",
					Flags: []Flag{{Name: "pretty", Kind: Bool, Short: "p", Usage: "print pretty"}},
					Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
				}},
			}},
		}, `flags "page" and "pretty" share the "p" shorthand`)
	})

	t.Run("a multi-character shorthand", func(t *testing.T) {
		expectPanic(t, &App{
			Name: "t", Version: "v1",
			Commands: []*Command{{
				Name: "probe", Short: "run one probe",
				Flags: []Flag{{Name: "verbose", Kind: Bool, Short: "vv", Usage: "log verbosely"}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) { return nil, nil },
			}},
		}, `flag "verbose" uses shorthand "vv", want one letter`)
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
			t.Errorf("shadowed shorthand: exit = %d, stderr: %s; want a clean run", code, stderr)
		}
	})
}
