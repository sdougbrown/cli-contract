package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckAgainstFixtureBinary builds the fixture CLI from the spec's
// conventions declaration, runs the checker against the built binary, and
// requires every derived case to hold. The declaration file is generated
// from the spec fixture at test time, so the checker is proven against
// the spec's own surface, not a copy that could drift.
func TestCheckAgainstFixtureBinary(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "spec", "conformance", "fixtures", "conventions.json"))
	if err != nil {
		t.Fatalf("read conventions fixture: %v", err)
	}
	var doc struct {
		Declaration json.RawMessage `json:"declaration"`
	}
	if err := json.Unmarshal(fixture, &doc); err != nil {
		t.Fatalf("parse conventions fixture: %v", err)
	}
	declPath := filepath.Join("testdata", "fixturecli", "declaration.json")
	if err := os.WriteFile(declPath, doc.Declaration, 0o644); err != nil {
		t.Fatalf("write declaration: %v", err)
	}
	t.Cleanup(func() { os.Remove(declPath) })

	bin := filepath.Join(t.TempDir(), "fixturecli")
	build := exec.Command("go", "build", "-o", bin, "./cmd/cli-contract/testdata/fixturecli")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture binary: %v\n%s", err, out)
	}

	checker := filepath.Join(t.TempDir(), "cli-contract")
	buildChecker := exec.Command("go", "build", "-o", checker, "./cmd/cli-contract")
	buildChecker.Dir = "../.."
	if out, err := buildChecker.CombinedOutput(); err != nil {
		t.Fatalf("build checker: %v\n%s", err, out)
	}

	run := exec.Command(checker, "check", bin)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("check failed:\n%s", out)
	}
	if !strings.Contains(string(out), "0 violations") {
		t.Errorf("checker output = %q, want 0 violations", out)
	}

	// A target that cannot expose a declaration is a checker usage
	// failure (exit 2), not a violation report.
	run = exec.Command(checker, "check", filepath.Join("testdata", "missing"))
	out, err = run.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
		t.Errorf("check of a non-runnable target: err = %v, output = %q; want exit 2", err, out)
	}
}
