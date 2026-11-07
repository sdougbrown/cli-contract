// Package check derives conformance cases from a schema document and runs
// them against a CLI as a black box. It knows nothing about the target's
// implementation: it reads the declared surface, invokes the binary, and
// judges exit codes and output classes.
package check

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/sdougbrown/cli-contract"
)

// Timeouts bound one invocation. A target that hangs must fail the case, not
// the checker: this runs untrusted binaries.
const (
	caseTimeout   = 10 * time.Second
	schemaTimeout = 30 * time.Second
)

// Stream class names a case may require. They mirror the fixture classes, so a
// black-box case and a fixture case state the same thing.
const (
	ClassEmpty     = "empty"
	ClassNonEmpty  = "non-empty"
	ClassPlainText = "plain-text"
	ClassJSONValue = "json-value"
	ClassAny       = ""
)

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
		{ID: "help:root", Argv: []string{"--help"}, Want: Want{Exit: 0, Stdout: ClassPlainText}},
		{ID: "unknown-flag:root", Argv: []string{"--cli-contract-unknown-flag"}, Want: Want{Exit: 2, Stderr: ClassNonEmpty}},
		{ID: "unknown-command:root", Argv: []string{"cli-contract-unknown-command"}, Want: Want{Exit: 2, Stderr: ClassNonEmpty}},
	}
	if doc.Version != "" {
		cases = append(cases, Case{
			ID:   "version:root",
			Argv: []string{"--version"},
			Want: Want{Exit: 0, Stdout: ClassNonEmpty},
		})
	}
	walk(doc.Commands, "", func(path string, cmd clicontract.CommandSchema) {
		names := strings.Fields(path)
		cases = append(cases,
			Case{
				ID:      "help:" + path,
				Command: path,
				Argv:    append(append([]string{}, names...), "--help"),
				Want:    Want{Exit: 0, Stdout: ClassPlainText},
			},
			Case{
				ID:      "unknown-flag:" + path,
				Command: path,
				Argv:    append(append([]string{}, names...), "--cli-contract-unknown-flag"),
				Want:    Want{Exit: 2, Stdout: errorOutputClass(cmd.Output), Stderr: ClassNonEmpty},
			},
		)
		if len(cmd.Subcommands) == 0 && hasRequiredPositional(cmd) {
			cases = append(cases, Case{
				ID:      "missing-positional:" + path,
				Command: path,
				Argv:    names,
				Want:    Want{Exit: 2, Stderr: ClassNonEmpty},
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
		return ClassJSONValue
	case clicontract.OutputNone:
		return ClassEmpty
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
// arguments; each case's argv is appended to it.
func Run(target []string, cases []Case) ([]Result, error) {
	if len(target) == 0 {
		return nil, errors.New("check: no target binary")
	}
	results := make([]Result, 0, len(cases))
	for _, c := range cases {
		argv := append(append([]string{}, target...), c.Argv...)
		ctx, cancel := context.WithTimeout(context.Background(), caseTimeout)
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		got := Got{Stdout: stdout.String(), Stderr: stderr.String()}
		if timedOut {
			results = append(results, Result{Case: c, Got: got, Detail: fmt.Sprintf("timed out after %s", caseTimeout)})
			continue
		}
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				return nil, fmt.Errorf("check: run %s: %w", strings.Join(argv, " "), err)
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
	switch class {
	case ClassAny:
		return true, ""
	case ClassEmpty:
		if strings.TrimSpace(got) != "" {
			return false, stream + " is not empty"
		}
	case ClassNonEmpty:
		if strings.TrimSpace(got) == "" {
			return false, stream + " is empty"
		}
	case ClassPlainText:
		if strings.TrimSpace(got) == "" {
			return false, stream + " is empty, want plain text"
		}
		if isJSONValue(got) {
			return false, stream + " is a JSON value, want plain text"
		}
	case ClassJSONValue:
		if !isJSONValue(got) {
			return false, stream + " is not one JSON value"
		}
	default:
		return false, fmt.Sprintf("unknown class %q", class)
	}
	return true, ""
}

func isJSONValue(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	var value any
	return json.Unmarshal([]byte(s), &value) == nil
}

// LoadDocument reads the target's schema document, either from a file or by
// running the target's schema command.
func LoadDocument(target []string, schemaFile, schemaCommand string) (*clicontract.SchemaDocument, error) {
	if schemaFile != "" {
		data, err := os.ReadFile(schemaFile)
		if err != nil {
			return nil, err
		}
		return clicontract.ParseSchemaDocument(data)
	}
	if len(target) == 0 {
		return nil, errors.New("check: no target binary")
	}
	if schemaCommand == "" {
		schemaCommand = "schema"
	}
	argv := append(append([]string{}, target...), schemaCommand)
	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("check: %s exited %d: %s", strings.Join(argv, " "), exit.ExitCode(), strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("check: run %s: %w", strings.Join(argv, " "), err)
	}
	return clicontract.ParseSchemaDocument(out)
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
