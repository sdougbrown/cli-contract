// Command cli-contract checks a binary against the CLI behavior contract
// (spec/spec.md): the black-box enforcer for the conformance spec.
//
//	cli-contract check <binary>
//
// The target exposes its declaration document through a `schema` command
// that writes it to stdout. The checker derives cases from the declared
// surface — help on every command, an unknown flag on every command, a
// missing required positional, --version — runs them against the binary,
// and reports every violation with the case name and what disagreed. It
// exits 0 when every case holds, 1 when any case is violated, and 2 when
// it could not check (the target could not run, or its schema command
// failed or emitted no declaration document).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sdougbrown/cli-contract/conformance"
)

// caseTimeout bounds one execution of the target.
const caseTimeout = 10 * time.Second

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	if len(argv) != 2 || argv[0] != "check" || argv[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: cli-contract check <binary>")
		return 2
	}
	target := argv[1]
	if !strings.ContainsRune(target, os.PathSeparator) {
		if found, err := exec.LookPath(target); err == nil {
			target = found
		}
	} else if abs, err := filepath.Abs(target); err == nil {
		target = abs
	}

	decl, err := fetchDeclaration(target, caseTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli-contract: %v\n", err)
		return 2
	}
	cases := deriveCases(decl)

	var violations []string
	for _, c := range cases {
		obs := executeCase(target, c, caseTimeout)
		check := conformance.Case{
			ID:               c.id,
			Argv:             c.argv,
			ExpectedExitCode: c.exitCode,
			Stdout:           c.stdout,
			Stderr:           c.stderr,
		}
		for _, problem := range conformance.Check(check, conformance.Outcome{
			ExitCode: obs.exitCode, Stdout: obs.stdout, Stderr: obs.stderr,
		}) {
			violations = append(violations, fmt.Sprintf("%s: %s", c.id, problem))
		}
	}

	name := decl.Name
	if name == "" {
		name = target
	}
	fmt.Fprintf(os.Stderr, "checked %s: %d derived cases, %d violations\n", name, len(cases), len(violations))
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s\n", v)
	}
	if len(violations) > 0 {
		return 1
	}
	return 0
}

// derivedCase is one case the checker derived from the declaration.
type derivedCase struct {
	id       string
	argv     []string
	exitCode int
	stdout   string
	stderr   string
}

// observed is what one execution of the target produced.
type observed struct {
	exitCode int
	stdout   string
	stderr   string
}

// fetchDeclaration runs the target's schema command and parses the
// document it writes to stdout.
func fetchDeclaration(binary string, timeout time.Duration) (*conformance.Declaration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "schema")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s schema failed: %v (stderr: %s)", binary, err, strings.TrimSpace(stderr.String()))
	}
	var decl conformance.Declaration
	if err := json.Unmarshal([]byte(stdout.String()), &decl); err != nil {
		return nil, fmt.Errorf("%s schema did not write one declaration document: %v", binary, err)
	}
	if decl.Version != 1 {
		return nil, fmt.Errorf("declaration version %d, want 1", decl.Version)
	}
	return &decl, nil
}

// deriveCases builds the case list from the declared surface: the spec §2
// conventions every conforming CLI must satisfy. Dispatch-failure cases
// expect the app's error envelope on stdout when the app is envelope-style
// (any leaf in the default json-value mode), nothing otherwise.
func deriveCases(d *conformance.Declaration) []derivedCase {
	envelope := envelopeStyle(d)
	failStdout := "empty"
	if envelope {
		failStdout = "json-value"
	}
	var cases []derivedCase
	if !d.Behavior.BareHelp {
		cases = append(cases, derivedCase{
			id: "bare-invocation-is-a-caller-error", argv: []string{},
			exitCode: 2, stdout: failStdout, stderr: "non-empty",
		})
	}
	if d.AppVersion != "" {
		cases = append(cases, derivedCase{
			id: "version-flag", argv: []string{"--version"},
			exitCode: 0, stdout: "plain-text", stderr: "empty",
		})
	}
	cases = append(cases, derivedCase{
		id: "unknown-command", argv: []string{"cli-contract-unknown-command"},
		exitCode: 2, stdout: failStdout, stderr: "non-empty",
	})
	declared := declaredFlagNames(d)
	var walk func(path []string, c conformance.Command)
	walk = func(path []string, c conformance.Command) {
		full := strings.Join(path, " ")
		cases = append(cases, derivedCase{
			id: "help-on-" + full, argv: append(append([]string{}, path...), "--help"),
			exitCode: 0, stdout: "plain-text", stderr: "empty",
		})
		if len(c.Subcommands) > 0 {
			for _, name := range sortedCommandNames(c.Subcommands) {
				walk(append(append([]string{}, path...), name), c.Subcommands[name])
			}
			return
		}
		// An unknown flag on any command is a caller error (spec §2.3).
		unknown := unknownFlagName(declared)
		cases = append(cases, derivedCase{
			id: "unknown-flag-on-" + full, argv: append(append([]string{}, path...), unknown),
			exitCode: 2, stdout: failStdout, stderr: "non-empty",
		})
		// A missing required positional is a caller error.
		for _, p := range c.Positionals {
			if p.Required {
				cases = append(cases, derivedCase{
					id: "missing-positional-on-" + full, argv: append([]string{}, path...),
					exitCode: 2, stdout: failStdout, stderr: "non-empty",
				})
				break
			}
		}
	}
	for _, name := range sortedCommandNames(d.Commands) {
		walk([]string{name}, d.Commands[name])
	}
	return cases
}

// envelopeStyle reports whether the app writes an error envelope on
// stdout: true unless every leaf declares a non-envelope output mode.
func envelopeStyle(d *conformance.Declaration) bool {
	envelope := false
	var walk func(commands map[string]conformance.Command)
	walk = func(commands map[string]conformance.Command) {
		for _, c := range commands {
			if len(c.Subcommands) > 0 {
				walk(c.Subcommands)
				continue
			}
			if c.Output == "" || c.Output == "json-value" {
				envelope = true
			}
		}
	}
	walk(d.Commands)
	return envelope
}

// unknownFlagName picks a long flag name the declaration does not use.
func unknownFlagName(declared map[string]bool) string {
	base := "--cli-contract-unknown-flag"
	if !declared[strings.TrimPrefix(base, "--")] {
		return base
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !declared[strings.TrimPrefix(candidate, "--")] {
			return candidate
		}
	}
}

// declaredFlagNames collects every flag name in the declaration, common
// and per-command, so the derived unknown flag cannot collide.
func declaredFlagNames(d *conformance.Declaration) map[string]bool {
	names := map[string]bool{}
	for name := range d.CommonFlags {
		names[name] = true
	}
	var walk func(commands map[string]conformance.Command)
	walk = func(commands map[string]conformance.Command) {
		for _, c := range commands {
			for name := range c.Flags {
				names[name] = true
			}
			walk(c.Subcommands)
		}
	}
	walk(d.Commands)
	return names
}

func sortedCommandNames(commands map[string]conformance.Command) []string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// executeCase runs one derived case against the binary.
func executeCase(binary string, c derivedCase, timeout time.Duration) observed {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, c.argv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	obs := observed{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		if exitErr, ok := err.(interface{ ExitCode() int }); ok {
			obs.exitCode = exitErr.ExitCode()
		} else {
			// A timeout or a start failure is a violation: an exit code
			// outside every vocabulary.
			obs.exitCode = -1
		}
	}
	return obs
}
