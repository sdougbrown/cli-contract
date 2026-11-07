package check

import (
	"os/exec"
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

// TestRunTimesOut pins the hang path: a target that outlives the case timeout
// is a failed case with an unobserved exit, not a checker failure.
func TestRunTimesOut(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep(1) on this platform")
	}
	cases := []Case{{ID: "hang:root", Argv: []string{"5"}, Want: Want{Exit: 0}}}
	results, err := run([]string{sleep}, cases, 100*time.Millisecond)
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

// TestLoadDocumentTimesOut pins the schema-command hang path: a command
// killed at the deadline has no observed exit, so the error must say it timed
// out rather than report a made-up exit code.
func TestLoadDocumentTimesOut(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep(1) on this platform")
	}
	_, err = loadDocument([]string{sleep}, "", "5", 100*time.Millisecond)
	if err == nil {
		t.Fatal("a timed-out schema command produced no error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want a timeout explanation", err)
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
