package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sdougbrown/cli-contract"
	"github.com/sdougbrown/cli-contract/internal/check"
)

// buildChecker builds this command into a temporary directory.
func buildChecker(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cli-contract")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build checker: %v", err)
	}
	return bin
}

// runCheckerRaw runs the checker, returning its exit code and raw stdout.
func runCheckerRaw(t *testing.T, bin string, argv ...string) (int, []byte) {
	t.Helper()
	cmd := exec.Command(bin, argv...)
	out, err := cmd.Output()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run checker: %v", err)
		}
		code = exit.ExitCode()
	}
	return code, out
}

// runChecker runs the checker, returning its exit code and the decoded report.
func runChecker(t *testing.T, bin string, argv ...string) (int, check.Report) {
	t.Helper()
	code, out := runCheckerRaw(t, bin, argv...)
	var report check.Report
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("decode report (%q): %v", out, err)
	}
	return code, report
}

// errorEnvelope is the failure shape the adapter writes instead of a report.
type errorEnvelope struct {
	Status string `json:"status"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// decodeErrorEnvelope decodes a failure envelope.
func decodeErrorEnvelope(t *testing.T, out []byte) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode error envelope (%q): %v", out, err)
	}
	return env
}

// buildFixture builds the stub target the schema-command tests run against.
func buildFixture(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fixture")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fixture")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build fixture: %v", err)
	}
	return bin
}

// writeTargetDocument writes the hand-written document the tests that read it
// from a file load, and returns its path.
func writeTargetDocument(t *testing.T) string {
	t.Helper()
	doc := filepath.Join(t.TempDir(), "schema.json")
	document := `{
	  "schema": "cli-contract/cli-schema/v1",
	  "name": "target",
	  "commands": {
	    "only": {"short": "do one thing", "use": "only", "url_positional": false}
	  },
	  "common_flags": {}
	}`
	if err := os.WriteFile(doc, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestAdapterErrorCodes pins the envelope split the reference adapters use:
// a caller mistake is a usage error, while a broken target and any other
// failure are not.
func TestAdapterErrorCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "usage", err: &clicontract.UsageError{Message: "bad schema file"}, want: "usage"},
		{name: "target failure", err: &check.TargetError{Err: errors.New("schema command exited 2")}, want: "internal"},
		{name: "internal", err: errors.New("boom"), want: "internal"},
	}
	for _, c := range cases {
		env, ok := adapter{}.ErrorResult("check", c.err).(map[string]any)
		if !ok {
			t.Fatalf("%s: ErrorResult returned %T, want map[string]any", c.name, adapter{}.ErrorResult("check", c.err))
		}
		errObj, ok := env["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s: error member = %T, want map[string]any", c.name, env["error"])
		}
		if code, _ := errObj["code"].(string); code != c.want {
			t.Errorf("%s: code = %q, want %q", c.name, code, c.want)
		}
	}
}

// TestAdapterExitCodes pins the exit split: a completed run's verdict decides
// 0 or 1, while a check that never produced a verdict — the caller's mistake
// or a broken target — is 2.
func TestAdapterExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		result any
		err    error
		want   int
	}{
		{name: "every case passed", result: check.Report{OK: true}, want: 0},
		{name: "a case failed", result: check.Report{OK: false}, want: 1},
		{name: "caller mistake", err: &clicontract.UsageError{Message: "bad schema file"}, want: 2},
		{name: "broken target", err: &check.TargetError{Err: errors.New("schema command exited 2")}, want: 2},
		{name: "unclassified handler failure", err: errors.New("boom"), want: 2},
	}
	for _, c := range cases {
		if got := (adapter{}).ExitCode(clicontract.Invocation{}, c.result, c.err); got != c.want {
			t.Errorf("%s: exit = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestCheckItself is the end-to-end proof: the checker's own declared surface
// conforms to the specification it enforces.
func TestCheckItself(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildChecker(t)

	code, report := runChecker(t, bin, "check", bin)
	if code != 0 || !report.OK {
		for _, c := range report.Cases {
			if !c.OK {
				t.Errorf("%s (%v): %s", c.ID, c.Argv, c.Detail)
			}
		}
		t.Fatalf("exit = %d, ok = %t: the checker does not conform to its own spec", code, report.OK)
	}
	if len(report.Cases) == 0 {
		t.Fatal("the checker derived no cases")
	}
}

// TestCheckKeepsEveryPositional pins the variadic TARGET: the binary plus its
// fixed arguments form the target, so a second positional must reach both the
// report and every invocation.
func TestCheckKeepsEveryPositional(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildChecker(t)
	target := buildFixture(t)

	code, report := runChecker(t, bin, "check", "--schema-file", writeTargetDocument(t), target, "extra")
	if code != 0 || !report.OK {
		t.Fatalf("exit = %d, ok = %t: the target with a fixed argument did not pass", code, report.OK)
	}
	want := []string{target, "extra"}
	if !slices.Equal(report.Target, want) {
		t.Fatalf("target = %v, want %v", report.Target, want)
	}
}

// TestCheckForwardsSchemaCommand pins the flag's forwarding: the document must
// come from the nominated subcommand, not the hardcoded default.
func TestCheckForwardsSchemaCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildChecker(t)
	target := buildFixture(t)

	code, report := runChecker(t, bin, "check", "--schema-command", "dump-schema", target)
	if code != 0 || !report.OK {
		for _, c := range report.Cases {
			if !c.OK {
				t.Errorf("%s (%v): %s", c.ID, c.Argv, c.Detail)
			}
		}
		t.Fatalf("exit = %d, ok = %t: the check did not run through dump-schema", code, report.OK)
	}
	if report.Schema != clicontract.SchemaDocumentV1 {
		t.Errorf("schema = %q, want %q", report.Schema, clicontract.SchemaDocumentV1)
	}
	if len(report.Cases) == 0 {
		t.Fatal("the checker derived no cases")
	}
}

// TestCheckSchemaFileWinsOverCommand pins the precedence rule: when both flags
// are given, the file is authoritative, so a command the target does not serve
// never runs.
func TestCheckSchemaFileWinsOverCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildChecker(t)
	target := buildFixture(t)

	code, report := runChecker(t, bin, "check",
		"--schema-file", writeTargetDocument(t),
		"--schema-command", "no-such-schema-cmd",
		target)
	if code != 0 || !report.OK {
		for _, c := range report.Cases {
			if !c.OK {
				t.Errorf("%s (%v): %s", c.ID, c.Argv, c.Detail)
			}
		}
		t.Fatalf("exit = %d, ok = %t: the schema file did not win over the schema command", code, report.OK)
	}
	if report.Schema != clicontract.SchemaDocumentV1 {
		t.Errorf("schema = %q, want %q", report.Schema, clicontract.SchemaDocumentV1)
	}
}

// TestCheckReportsNonConformingTarget keeps the verdict honest: a binary that
// ignores the surface fails, and the report names the cases.
func TestCheckReportsNonConformingTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	target, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true(1) on this platform")
	}
	bin := buildChecker(t)

	code, report := runChecker(t, bin, "check", "--schema-file", writeTargetDocument(t), target)
	if code != 1 {
		t.Fatalf("exit = %d, want 1: a non-conforming target is an operational failure", code)
	}
	if report.OK {
		t.Fatal("report claims a non-conforming target passed")
	}
	var failed int
	for _, c := range report.Cases {
		if !c.OK {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("no case failed against a target that ignores the surface")
	}
}

// TestCheckCouldNotRunExitsTwo pins the documented split: a check that never
// ran is a caller error (2), not a failed check (1).
func TestCheckCouldNotRunExitsTwo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildChecker(t)
	missing := filepath.Join(t.TempDir(), "missing.json")
	code, _ := runChecker(t, bin, "check", "--schema-file", missing, "true")
	if code != 2 {
		t.Fatalf("exit = %d, want 2: the check could not run", code)
	}
}

// TestCheckMissingTargetExitsTwo pins the launch-failure path: the document
// loads, but no case can start because the target cannot be executed.
func TestCheckMissingTargetExitsTwo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildChecker(t)

	target := filepath.Join(t.TempDir(), "missing-target")
	code, _ := runChecker(t, bin, "check", "--schema-file", writeTargetDocument(t), target)
	if code != 2 {
		t.Fatalf("exit = %d, want 2: the check could not run", code)
	}
}

// TestCheckSchemaFailureReportsNonUsage pins the wire class for a target-side
// failure: a schema command that exits non-zero is a broken target, not a
// caller mistake, so the envelope must not claim usage even though the check
// still exits 2.
func TestCheckSchemaFailureReportsNonUsage(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildChecker(t)

	code, out := runCheckerRaw(t, bin, "check", "--schema-command", "no-such-schema-command", bin)
	if code != 2 {
		t.Fatalf("exit = %d, want 2: the check could not run", code)
	}
	env := decodeErrorEnvelope(t, out)
	if env.Status != "error" {
		t.Errorf("status = %q, want %q", env.Status, "error")
	}
	if env.Error.Code == "usage" {
		t.Errorf("error code = %q: a failing schema command is not a caller mistake", env.Error.Code)
	}
	if env.Error.Code != "internal" {
		t.Errorf("error code = %q, want %q", env.Error.Code, "internal")
	}
}

// TestCheckBadSchemaFileReportsUsage pins the caller side of the same split:
// an unreadable --schema-file is the invocation's own fault.
func TestCheckBadSchemaFileReportsUsage(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildChecker(t)
	missing := filepath.Join(t.TempDir(), "missing.json")

	code, out := runCheckerRaw(t, bin, "check", "--schema-file", missing, "true")
	if code != 2 {
		t.Fatalf("exit = %d, want 2: the check could not run", code)
	}
	env := decodeErrorEnvelope(t, out)
	if env.Error.Code != "usage" {
		t.Errorf("error code = %q, want %q: an unreadable schema file is a caller mistake", env.Error.Code, "usage")
	}
}
