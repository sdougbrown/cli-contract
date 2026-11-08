package check

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sdougbrown/cli-contract"
	"github.com/sdougbrown/cli-contract/internal/streamclass"
)

// nestedDoc declares one parent with one leaf subcommand, so every derived
// case for the leaf has to carry the parent's name in its path and argv.
func nestedDoc() *clicontract.SchemaDocument {
	return &clicontract.SchemaDocument{
		Schema: clicontract.SchemaDocumentV1,
		Name:   "t",
		Commands: map[string]clicontract.CommandSchema{
			"store": {
				Short: "manage stores",
				Use:   "store",
				Subcommands: map[string]clicontract.CommandSchema{
					"reset": {
						Short:       "reset a store",
						Use:         "reset",
						Positionals: []clicontract.Positional{{Name: "TARGET", Required: true}},
					},
				},
			},
		},
	}
}

// TestDeriveNestedSubcommandPaths keeps the recursion honest: a subcommand's
// cases are keyed by and invoked with the full path, not its bare name.
func TestDeriveNestedSubcommandPaths(t *testing.T) {
	want := map[string][]string{
		"help:store":                     {"store", "--help"},
		"unknown-flag:store":             {"store", "--cli-contract-unknown-flag"},
		"help:store reset":               {"store", "reset", "--help"},
		"unknown-flag:store reset":       {"store", "reset", "--cli-contract-unknown-flag"},
		"missing-positional:store reset": {"store", "reset"},
	}
	derived := Derive(nestedDoc())
	byID := make(map[string]Case, len(derived))
	ids := make([]string, 0, len(derived))
	for _, c := range derived {
		byID[c.ID] = c
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	for id, argv := range want {
		c, ok := byID[id]
		if !ok {
			t.Errorf("Derive produced no %q case, got %v", id, ids)
			continue
		}
		if !slices.Equal(c.Argv, argv) {
			t.Errorf("%s: argv = %v, want %v", id, c.Argv, argv)
		}
	}
	// The parent has subcommands, so it takes no missing-positional case.
	if _, ok := byID["missing-positional:store"]; ok {
		t.Error("Derive emitted a missing-positional case for a parent command")
	}
}

// TestDeriveVersionLine pins the version branch: a document declaring a
// version requires a --version case that prints something, and a document
// without one must not invent that case.
func TestDeriveVersionLine(t *testing.T) {
	doc := nestedDoc()
	doc.Version = "1.2.3"
	var version Case
	found := false
	for _, c := range Derive(doc) {
		if c.ID == "version:root" {
			version, found = c, true
		}
	}
	if !found {
		t.Fatal("Derive produced no version:root case for a document declaring a version")
	}
	if want := []string{"--version"}; !slices.Equal(version.Argv, want) {
		t.Errorf("argv = %v, want %v", version.Argv, want)
	}
	if version.Want.Exit != 0 {
		t.Errorf("exit = %d, want 0", version.Want.Exit)
	}
	if version.Want.Stdout != streamclass.NonEmpty {
		t.Errorf("stdout class = %q, want %q", version.Want.Stdout, streamclass.NonEmpty)
	}

	for _, c := range Derive(nestedDoc()) {
		if c.ID == "version:root" {
			t.Fatal("Derive emitted a version:root case for a document with no version")
		}
	}
}

// TestRunTimesOut pins the hang path: a target that outlives the case timeout
// is a failed case with an unobserved exit, not a checker failure.
func TestRunTimesOut(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep(1) on this platform")
	}
	cases := []Case{{ID: "hang:root", Argv: []string{"5"}, Want: Want{Exit: 0}}}
	results, err := run(context.Background(), []string{sleep}, cases, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("run returned %d results, want 1", len(results))
	}
	got := results[0]
	if got.OK {
		t.Error("a timed-out case passed")
	}
	if got.Got.Exit != timedOutExit {
		t.Errorf("exit = %d, want %d: a killed process's exit was never observed", got.Got.Exit, timedOutExit)
	}
	if !strings.Contains(got.Detail, "timed out") {
		t.Errorf("detail = %q, want a timeout explanation", got.Detail)
	}
}

// TestRunTimesOutForkedChild pins the fork path: the deadline kills only the
// direct child, so a backgrounded grandchild holding the inherited pipes must
// not keep the checker blocked long past it.
func TestRunTimesOutForkedChild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh(1) on this platform")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep(1) on this platform")
	}
	cases := []Case{{ID: "fork:root", Argv: []string{"-c", "sleep 5 & exit 0"}, Want: Want{Exit: 0}}}
	start := time.Now()
	results, err := run(context.Background(), []string{sh}, cases, 100*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if elapsed >= 4*time.Second {
		t.Errorf("run took %s with a forked child holding the pipes; want well under the child's 5s", elapsed)
	}
	if len(results) != 1 {
		t.Fatalf("run returned %d results, want 1", len(results))
	}
	got := results[0]
	if got.OK {
		t.Error("a case whose pipes outlived the deadline passed")
	}
	if got.Got.Exit != timedOutExit {
		t.Errorf("exit = %d, want %d: a case cut off at the deadline observed no exit", got.Got.Exit, timedOutExit)
	}
	if !strings.Contains(got.Detail, "timed out") {
		t.Errorf("detail = %q, want a timeout explanation", got.Detail)
	}
}

// TestRunStopsOnCancellation pins the signal path: a cancelled parent context
// kills the case in flight and aborts the run, instead of letting the target
// run out its per-case timeout before the caller can exit.
func TestRunStopsOnCancellation(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep(1) on this platform")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cases := []Case{{ID: "hang:root", Argv: []string{"30"}, Want: Want{Exit: 0}}}
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	results, err := run(ctx, []string{sleep}, cases, time.Minute)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("run returned %d results after cancellation, want an error", len(results))
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %q, want a cancellation", err)
	}
	if elapsed >= 5*time.Second {
		t.Errorf("run took %s after cancellation; want well under the per-case timeout", elapsed)
	}
}

// TestLoadDocumentTimesOut pins the schema-command hang path: a command
// killed at the deadline has no observed exit, so the error must say it timed
// out rather than report a made-up exit code.
func TestLoadDocumentTimesOut(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep(1) on this platform")
	}
	_, err = loadDocument(context.Background(), []string{sleep}, "", "5", 100*time.Millisecond)
	if err == nil {
		t.Fatal("a timed-out schema command produced no error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want a timeout explanation", err)
	}
}

// TestLoadDocumentReportsStderr pins the failure diagnostic: when the schema
// command exits non-zero, its stderr must reach the error message so the user
// can tell why the document could not be read.
func TestLoadDocumentReportsStderr(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh(1) on this platform")
	}
	const diagnostic = "schema: cannot render the document"
	script := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(script, []byte("printf '%s\\n' '"+diagnostic+"' >&2; exit 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = loadDocument(context.Background(), []string{sh, script}, "", "", time.Second)
	if err == nil {
		t.Fatal("a failing schema command produced no error")
	}
	if !strings.Contains(err.Error(), diagnostic) {
		t.Errorf("error = %q, want the command's stderr", err)
	}
	if !strings.Contains(err.Error(), "exited 3") {
		t.Errorf("error = %q, want the observed exit code", err)
	}
}

// TestJudgeClasses pins the JSON-vs-plain-text boundary: help output must not
// be a JSON document, and a JSON-success case must reject prose.
func TestJudgeClasses(t *testing.T) {
	cases := []struct {
		name   string
		want   Want
		got    Got
		ok     bool
		detail string
	}{
		{
			name:   "plain text rejects a JSON value",
			want:   Want{Exit: 0, Stdout: streamclass.PlainText},
			got:    Got{Stdout: `{"a":1}`},
			detail: "stdout is a JSON value, want plain text",
		},
		{
			name: "plain text accepts prose",
			want: Want{Exit: 0, Stdout: streamclass.PlainText},
			got:  Got{Stdout: "usage: t\n"},
			ok:   true,
		},
		{
			name: "json value accepts a JSON value",
			want: Want{Exit: 0, Stdout: streamclass.JSONValue},
			got:  Got{Stdout: `{"status":"ok"}`},
			ok:   true,
		},
		{
			name:   "json value rejects prose",
			want:   Want{Exit: 0, Stdout: streamclass.JSONValue},
			got:    Got{Stdout: "status: ok\n"},
			detail: "stdout is not one JSON value",
		},
		{
			// The classes are satisfiable here, so only the exit branch can
			// produce this verdict.
			name:   "exit mismatch fails before the stream classes",
			want:   Want{Exit: 2, Stdout: streamclass.NonEmpty},
			got:    Got{Exit: 0, Stdout: "usage: t\n"},
			detail: "exit 0, want 2",
		},
	}
	for _, c := range cases {
		ok, detail := judge(c.want, c.got)
		if ok != c.ok || detail != c.detail {
			t.Errorf("%s: judge = (%t, %q), want (%t, %q)", c.name, ok, detail, c.ok, c.detail)
		}
	}
}
