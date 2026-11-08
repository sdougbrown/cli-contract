// Package check derives conformance cases from a schema document and runs
// them against a CLI as a black box. It knows nothing about the target's
// implementation: it reads the declared surface, invokes the binary, and
// judges exit codes and output classes.
package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/sdougbrown/cli-contract"
	"github.com/sdougbrown/cli-contract/internal/streamclass"
)

// Timeouts bound one invocation. A target that hangs must fail the case, not
// the checker: this runs untrusted binaries. The context deadline kills the
// direct child, but that alone does not unblock Wait: a forked grandchild that
// inherited stdout or stderr keeps the pipes open. pipeGrace caps the drain
// that follows, so one invocation is bounded by its timeout plus this grace.
const (
	caseTimeout   = 10 * time.Second
	schemaTimeout = 30 * time.Second
	pipeGrace     = time.Second
)

// timedOutExit is the sentinel Got.Exit carries when the process was killed
// before it exited: no real exit code was observed, and the JSON report must
// not claim 0.
const timedOutExit = -1

// ClassAny requires nothing of a stream.
const ClassAny = ""

// TargetError marks a failure the target caused: its schema command exited
// non-zero, timed out, or printed an unreadable document, or a case could not
// be launched or was aborted. The check could not run, but the invocation was
// well formed; a caller's own bad input does not carry this type.
type TargetError struct{ Err error }

func (e *TargetError) Error() string { return e.Err.Error() }

// Unwrap keeps the cause reachable: context.Canceled from an aborted run, or
// the exec error behind a launch failure.
func (e *TargetError) Unwrap() error { return e.Err }

// targetErrorf builds a target-side failure.
func targetErrorf(format string, args ...any) error {
	return &TargetError{Err: fmt.Errorf(format, args...)}
}

// Want is the behavior one case requires.
type Want struct {
	Exit   int    `json:"exit"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}

// Case is one derived invocation and its required behavior.
type Case struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Argv    []string `json:"argv"`
	Want    Want     `json:"want"`
}

// Got is what the target actually did.
type Got struct {
	Exit   int    `json:"exit"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// Result is one case's verdict.
type Result struct {
	Case
	Got    Got    `json:"got"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report is the whole run.
type Report struct {
	Target []string `json:"target"`
	Schema string   `json:"schema"`
	OK     bool     `json:"ok"`
	Cases  []Result `json:"cases"`
}

// Derive builds the cases every conforming CLI must pass from its declared
// surface: help on every command, an unknown flag on every command, an unknown
// command, a missing required positional on every leaf that has one, and the
// version line when the document declares a version.
//
// Signal cases are deliberately absent: they need a command that stays alive
// long enough to be signalled, which the surface does not declare.
func Derive(doc *clicontract.SchemaDocument) []Case {
	cases := []Case{
		{ID: "help:root", Argv: []string{"--help"}, Want: Want{Exit: 0, Stdout: streamclass.PlainText}},
		{ID: "unknown-flag:root", Argv: []string{"--cli-contract-unknown-flag"}, Want: Want{Exit: 2, Stderr: streamclass.NonEmpty}},
		{ID: "unknown-command:root", Argv: []string{"cli-contract-unknown-command"}, Want: Want{Exit: 2, Stderr: streamclass.NonEmpty}},
	}
	if doc.Version != "" {
		cases = append(cases, Case{
			ID:   "version:root",
			Argv: []string{"--version"},
			Want: Want{Exit: 0, Stdout: streamclass.NonEmpty},
		})
	}
	walk(doc.Commands, "", func(path string, cmd clicontract.CommandSchema) {
		names := strings.Fields(path)
		cases = append(cases,
			Case{
				ID:      "help:" + path,
				Command: path,
				Argv:    append(append([]string{}, names...), "--help"),
				Want:    Want{Exit: 0, Stdout: streamclass.PlainText},
			},
			Case{
				ID:      "unknown-flag:" + path,
				Command: path,
				Argv:    append(append([]string{}, names...), "--cli-contract-unknown-flag"),
				Want:    Want{Exit: 2, Stdout: errorOutputClass(cmd.Output), Stderr: streamclass.NonEmpty},
			},
		)
		if len(cmd.Subcommands) == 0 && hasRequiredPositional(cmd) {
			cases = append(cases, Case{
				ID:      "missing-positional:" + path,
				Command: path,
				Argv:    names,
				Want:    Want{Exit: 2, Stdout: errorOutputClass(cmd.Output), Stderr: streamclass.NonEmpty},
			})
		}
	})
	return cases
}

// walk visits every command with its full path, parents included.
func walk(commands map[string]clicontract.CommandSchema, prefix string, visit func(path string, cmd clicontract.CommandSchema)) {
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		cmd := commands[name]
		path := name
		if prefix != "" {
			path = prefix + " " + name
		}
		visit(path, cmd)
		if len(cmd.Subcommands) > 0 {
			walk(cmd.Subcommands, path, visit)
		}
	}
}

// errorOutputClass is the stdout class a declared output mode promises on a
// failure. A raw command's bytes are its own, and an undeclared mode promises
// nothing, so both impose no class.
func errorOutputClass(output *clicontract.Output) string {
	if output == nil {
		return ClassAny
	}
	switch output.Mode {
	case clicontract.OutputJSON:
		return streamclass.JSONValue
	case clicontract.OutputNone:
		return streamclass.Empty
	default:
		return ClassAny
	}
}

func hasRequiredPositional(cmd clicontract.CommandSchema) bool {
	for _, p := range cmd.Positionals {
		if p.Required {
			return true
		}
	}
	return false
}

// Run executes every case against target. target is the binary plus any fixed
// arguments; each case's argv is appended to it. Cancelling ctx kills the case
// in flight and aborts the run; a case that outlives caseTimeout is a failed
// case, not an aborted run.
func Run(ctx context.Context, target []string, cases []Case) ([]Result, error) {
	return run(ctx, target, cases, caseTimeout)
}

// run is Run under a caller-chosen per-case timeout.
func run(ctx context.Context, target []string, cases []Case, timeout time.Duration) ([]Result, error) {
	if len(target) == 0 {
		return nil, errors.New("check: no target binary")
	}
	results := make([]Result, 0, len(cases))
	for _, c := range cases {
		argv := append(append([]string{}, target...), c.Argv...)
		caseCtx, cancel := context.WithTimeout(ctx, timeout)
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(caseCtx, argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.WaitDelay = pipeGrace
		err := cmd.Run()
		parentErr := ctx.Err()
		timedOut := parentErr == nil && caseCtx.Err() == context.DeadlineExceeded
		cancel()
		if parentErr != nil {
			return nil, targetErrorf("check: run %s: %w", strings.Join(argv, " "), parentErr)
		}
		got := Got{Stdout: stdout.String(), Stderr: stderr.String()}
		if timedOut {
			got.Exit = timedOutExit
			results = append(results, Result{Case: c, Got: got, Detail: fmt.Sprintf("timed out after %s", timeout)})
			continue
		}
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				return nil, targetErrorf("check: run %s: %w", strings.Join(argv, " "), err)
			}
			got.Exit = exit.ExitCode()
		}
		ok, detail := judge(c.Want, got)
		results = append(results, Result{Case: c, Got: got, OK: ok, Detail: detail})
	}
	return results, nil
}

// judge compares one case's requirement to what happened.
func judge(want Want, got Got) (bool, string) {
	if got.Exit != want.Exit {
		return false, fmt.Sprintf("exit %d, want %d", got.Exit, want.Exit)
	}
	if ok, detail := checkClass("stdout", want.Stdout, got.Stdout); !ok {
		return false, detail
	}
	if ok, detail := checkClass("stderr", want.Stderr, got.Stderr); !ok {
		return false, detail
	}
	return true, ""
}

// checkClass tests one stream against its required class. An empty class
// imposes no requirement.
func checkClass(stream, class, got string) (bool, string) {
	if class == ClassAny {
		return true, ""
	}
	reason := streamclass.Judge(class, got)
	if reason.OK() {
		return true, ""
	}
	if reason == streamclass.ReasonUnknownClass {
		return false, fmt.Sprintf("unknown class %q", class)
	}
	return false, stream + " " + reason.String()
}

// LoadDocument reads the target's schema document, either from a file or by
// running the target's schema command. Cancelling ctx kills the schema command
// in flight and aborts the load.
func LoadDocument(ctx context.Context, target []string, schemaFile, schemaCommand string) (*clicontract.SchemaDocument, error) {
	return loadDocument(ctx, target, schemaFile, schemaCommand, schemaTimeout)
}

// loadDocument is LoadDocument under a caller-chosen schema-command timeout.
func loadDocument(ctx context.Context, target []string, schemaFile, schemaCommand string, timeout time.Duration) (*clicontract.SchemaDocument, error) {
	if schemaFile != "" {
		// A schema file is the caller's own input: an unreadable path or an
		// unparseable document here is a caller mistake, not a target failure.
		data, err := os.ReadFile(schemaFile)
		if err != nil {
			return nil, err
		}
		return clicontract.ParseSchemaDocument(data)
	}
	if len(target) == 0 {
		return nil, errors.New("check: no target binary")
	}
	argv := append(append([]string{}, target...), schemaCommand)
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = pipeGrace
	out, err := cmd.Output()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, targetErrorf("check: %s: %w", strings.Join(argv, " "), ctxErr)
	}
	if cmdCtx.Err() == context.DeadlineExceeded {
		return nil, targetErrorf("check: %s timed out after %s", strings.Join(argv, " "), timeout)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, targetErrorf("check: %s exited %d: %s", strings.Join(argv, " "), exit.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return nil, targetErrorf("check: run %s: %w", strings.Join(argv, " "), err)
	}
	doc, err := clicontract.ParseSchemaDocument(out)
	if err != nil {
		// The target printed something that is not a schema document.
		return nil, &TargetError{Err: err}
	}
	return doc, nil
}

// AllOK reports whether every case passed.
func AllOK(results []Result) bool {
	for _, r := range results {
		if !r.OK {
			return false
		}
	}
	return true
}
