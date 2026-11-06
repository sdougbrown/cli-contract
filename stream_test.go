package clicontract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStreamLifecycle(t *testing.T) {
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(ctx context.Context, inv Invocation, out, errOut io.Writer) (any, error) {
		io.WriteString(out, "one\ntwo\n")
		return map[string]any{"status": "ok"}, nil
	}}}}
	a := &testAdapter{notice: []string{"starting"}}
	var out, errOut bytes.Buffer
	if code := Run(app, a, []string{"watch"}, &out, &errOut); code != 0 || out.String() != "one\ntwo\n" || errOut.String() != "starting\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if len(a.wrote) != 0 {
		t.Fatal("adapter wrote an envelope")
	}
}

func TestStreamErrorsAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"usage", UsageErrorf("bad input"), 2},
		{"operational", errors.New("broken"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(ctx context.Context, inv Invocation, out, _ io.Writer) (any, error) {
				io.WriteString(out, "batch\n")
				return nil, tc.err
			}}}}
			var out, errOut bytes.Buffer
			code := Run(app, &testAdapter{}, []string{"watch"}, &out, &errOut)
			if code != tc.code || out.String() != "batch\n" || !strings.Contains(errOut.String(), tc.err.Error()) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
	t.Run("deadline", func(t *testing.T) {
		app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(ctx context.Context, inv Invocation, out, _ io.Writer) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}}}
		a := &testAdapter{deadline: func(ctx context.Context, _ Invocation) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, time.Millisecond)
		}}
		var out, errOut bytes.Buffer
		if code := Run(app, a, []string{"watch"}, &out, &errOut); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "deadline exceeded") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
}

type brokenStreamWriter struct{}

func (brokenStreamWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

func TestStreamWriteFailure(t *testing.T) {
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(ctx context.Context, inv Invocation, out, _ io.Writer) (any, error) {
		_, _ = io.WriteString(out, "batch\n")
		return map[string]any{"status": "ok"}, nil
	}}}}
	var errOut bytes.Buffer
	if code := Run(app, &testAdapter{}, []string{"watch"}, brokenStreamWriter{}, &errOut); code != ExitOperational || !strings.Contains(errOut.String(), "write failed") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

type rejectingStreamAdapter struct{ testAdapter }

func (a *rejectingStreamAdapter) Validate(Invocation) error { return errors.New("invalid invocation") }

func TestStreamStartupFailure(t *testing.T) {
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(context.Context, Invocation, io.Writer, io.Writer) (any, error) {
		t.Fatal("stream started")
		return nil, nil
	}}}}
	var out, errOut bytes.Buffer
	code := Run(app, &rejectingStreamAdapter{}, []string{"watch"}, &out, &errOut)
	if code != 2 || !strings.Contains(out.String(), "usage") || !strings.Contains(errOut.String(), "invalid invocation") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestNestedStreamAndParse(t *testing.T) {
	called := false
	app := &App{Name: "test", Commands: []*Command{{Name: "group", Short: "group", Subcommands: []*Command{{Name: "watch", Short: "watch", Stream: func(_ context.Context, inv Invocation, out, _ io.Writer) (any, error) {
		called = true
		io.WriteString(out, inv.Command+"/"+inv.Sub)
		return map[string]any{"status": "ok"}, nil
	}}}}}}
	inv, err := Parse(app, []string{"group", "watch"})
	if err != nil || called || inv.Command != "group" || inv.Sub != "watch" {
		t.Fatalf("inv=%+v called=%v err=%v", inv, called, err)
	}
	var out, errOut bytes.Buffer
	if code := Run(app, &testAdapter{}, []string{"group", "watch"}, &out, &errOut); code != 0 || out.String() != "group/watch" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

type streamStatusAdapter struct{ testAdapter }

func (a *streamStatusAdapter) ExitCode(_ Invocation, result any, err error) int {
	if err != nil {
		return 1
	}
	if result == "changed" {
		return 3
	}
	return 0
}

func TestStreamAdapterExitStatus(t *testing.T) {
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(_ context.Context, _ Invocation, out, _ io.Writer) (any, error) {
		io.WriteString(out, "batch\n")
		return "changed", nil
	}}}}
	var out, errOut bytes.Buffer
	if code := Run(app, &streamStatusAdapter{}, []string{"watch"}, &out, &errOut); code != 3 || out.String() != "batch\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

type partialStreamWriter struct{ buf bytes.Buffer }

func (w *partialStreamWriter) Write(p []byte) (int, error) {
	if w.buf.Len() == 0 {
		return w.buf.Write(p)
	}
	return 0, errors.New("write failed")
}

func TestStreamPartialOutputFailure(t *testing.T) {
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(_ context.Context, _ Invocation, out, _ io.Writer) (any, error) {
		io.WriteString(out, "first\n")
		_, err := io.WriteString(out, "second\n")
		return nil, err
	}}}}
	var out partialStreamWriter
	var errOut bytes.Buffer
	code := Run(app, &testAdapter{}, []string{"watch"}, &out, &errOut)
	if code != 1 || out.buf.String() != "first\n" || !strings.Contains(errOut.String(), "write failed") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.buf.String(), errOut.String())
	}
}

type afterWriteStreamAdapter struct{ testAdapter }

func (a *afterWriteStreamAdapter) AfterWrite(Invocation, any, io.Writer) {
	a.after = append(a.after, "summary")
}

func TestStreamAfterWrite(t *testing.T) {
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(_ context.Context, _ Invocation, out, _ io.Writer) (any, error) {
		io.WriteString(out, "batch\n")
		return map[string]any{"status": "ok"}, nil
	}}}}
	a := &afterWriteStreamAdapter{}
	if code := Run(app, a, []string{"watch"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 || len(a.after) != 1 {
		t.Fatalf("code=%d after=%v, want the summary after the stream output", code, a.after)
	}
	a = &afterWriteStreamAdapter{}
	var errOut bytes.Buffer
	if code := Run(app, a, []string{"watch"}, brokenStreamWriter{}, &errOut); code != ExitOperational || len(a.after) != 0 {
		t.Fatalf("code=%d after=%v stderr=%q, want the write failure to skip the summary", code, a.after, errOut.String())
	}
}

func TestStreamWriteFailureKeepsHandlerError(t *testing.T) {
	handlerErr := errors.New("handler stopped")
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(_ context.Context, _ Invocation, out, _ io.Writer) (any, error) {
		_, _ = io.WriteString(out, "batch\n")
		return nil, handlerErr
	}}}}
	var errOut bytes.Buffer
	code := Run(app, &testAdapter{}, []string{"watch"}, brokenStreamWriter{}, &errOut)
	if code != ExitOperational {
		t.Fatalf("code=%d, want ExitOperational: the write failure owns the exit code", code)
	}
	if !strings.Contains(errOut.String(), handlerErr.Error()) || strings.Contains(errOut.String(), "write failed") {
		t.Fatalf("stderr=%q, want the handler's error as the diagnostic", errOut.String())
	}
}

func TestStreamWriteFailureOwnsExitCode(t *testing.T) {
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(_ context.Context, _ Invocation, out, _ io.Writer) (any, error) {
		_, _ = io.WriteString(out, "batch\n")
		return nil, UsageErrorf("bad input")
	}}}}
	var errOut bytes.Buffer
	if code := Run(app, &testAdapter{}, []string{"watch"}, brokenStreamWriter{}, &errOut); code != ExitOperational {
		t.Fatalf("code=%d stderr=%q, want ExitOperational: the write failure outranks the usage classification", code, errOut.String())
	}
}

func TestStreamSignalAfterOutput(t *testing.T) {
	app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(_ context.Context, _ Invocation, out, _ io.Writer) (any, error) {
		io.WriteString(out, "ready\n")
		return map[string]any{"status": "ok"}, nil
	}}}}
	a := &testAdapter{seams: &testSeams{signal: syscall.SIGINT}}
	var out, errOut bytes.Buffer
	if code := Run(app, a, []string{"watch"}, &out, &errOut); code != 130 || out.String() != "ready\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q, want the signal to win with the output kept", code, out.String(), errOut.String())
	}
}

func TestStreamSignals(t *testing.T) {
	if os.Getenv("STREAM_SIGNAL_CHILD") == "1" {
		app := &App{Name: "test", Commands: []*Command{{Name: "watch", Short: "watch", Stream: func(ctx context.Context, _ Invocation, out, _ io.Writer) (any, error) {
			io.WriteString(out, "ready\n")
			<-ctx.Done()
			return nil, ctx.Err()
		}}}}
		os.Exit(Run(app, &testAdapter{}, []string{"watch"}, os.Stdout, os.Stderr))
	}
	for _, tc := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGINT, 130}, {syscall.SIGTERM, 143}} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStreamSignals$")
			cmd.Env = append(os.Environ(), "STREAM_SIGNAL_CHILD=1")
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			ready := make([]byte, len("ready\n"))
			if _, err := io.ReadFull(out, ready); err != nil {
				t.Fatal(err)
			}
			if string(ready) != "ready\n" {
				t.Fatalf("stdout=%q", ready)
			}
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
				t.Fatalf("wait=%v, want exit %d", err, tc.code)
			}
		})
	}
}
