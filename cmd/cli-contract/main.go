// Command cli-contract checks a CLI against the cli-contract specification.
//
//	cli-contract check <target...> [--schema-file <path>]
//	cli-contract schema
//
// The checker reads the target's schema document, derives the cases the
// declared surface implies, and runs them against the binary as a black box.
// Exit 0 means every case passed, 1 means at least one failed, and 2 means the
// check could not run.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/sdougbrown/cli-contract"
	"github.com/sdougbrown/cli-contract/internal/check"
)

// version is the checker's own version line; release builds set it.
var version = "dev"

func main() {
	os.Exit(clicontract.Run(app(), adapter{}, os.Args[1:], os.Stdout, os.Stderr))
}

// adapter writes the report as one JSON value and maps a failed check to the
// operational exit code, so a conformance failure is distinguishable from a
// caller mistake.
type adapter struct{}

func (adapter) ErrorResult(command string, err error) any {
	code := "internal"
	var usage *clicontract.UsageError
	if errors.As(err, &usage) {
		code = "usage"
	}
	return map[string]any{
		"command": command,
		"status":  "error",
		"error":   map[string]any{"code": code, "message": err.Error()},
	}
}

func (adapter) Write(w io.Writer, result any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func (adapter) ExitCode(inv clicontract.Invocation, result any, handlerErr error) int {
	if handlerErr != nil {
		var usage *clicontract.UsageError
		if errors.As(handlerErr, &usage) {
			return 2
		}
		return clicontract.ExitOperational
	}
	report, ok := result.(check.Report)
	if ok && !report.OK {
		return clicontract.ExitOperational
	}
	return clicontract.ExitOK
}

func (adapter) Deadline(ctx context.Context, inv clicontract.Invocation) (context.Context, context.CancelFunc) {
	return ctx, func() {}
}

func (adapter) BeforeHandler(inv clicontract.Invocation, stderr io.Writer) {}

func (adapter) Validate(inv clicontract.Invocation) error { return nil }

func app() *clicontract.App {
	var self *clicontract.App
	self = &clicontract.App{
		Name:      "cli-contract",
		Version:   version,
		RootShort: "check a CLI against the cli-contract specification",
		Commands: []*clicontract.Command{
			{
				Name:        "check",
				Short:       "run the conformance cases a CLI's schema document implies",
				Positionals: []clicontract.Positional{{Name: "TARGET", Required: true, Variadic: true}},
				Flags: []clicontract.Flag{
					{Name: "schema-file", Kind: clicontract.String, Usage: "read the schema document from a file instead of running the target's schema command"},
					{Name: "schema-command", Kind: clicontract.String, Usage: "command that prints the schema document", Default: "schema"},
				},
				Output: clicontract.Output{Mode: clicontract.OutputJSON, SuccessExitCodes: []int{0}},
				Handle: func(ctx context.Context, inv clicontract.Invocation, _ io.Writer) (any, error) {
					target := inv.Args
					doc, err := check.LoadDocument(target, inv.Flags["schema-file"], inv.Flags["schema-command"])
					if err != nil {
						return nil, &clicontract.UsageError{Message: err.Error()}
					}
					results, err := check.Run(target, check.Derive(doc))
					if err != nil {
						return nil, &clicontract.UsageError{Message: err.Error()}
					}
					return check.Report{
						Target: target,
						Schema: doc.Schema,
						OK:     check.AllOK(results),
						Cases:  results,
					}, nil
				},
			},
			{
				Name:  "schema",
				Short: "print this tool's schema document",
				Raw: func(ctx context.Context, inv clicontract.Invocation, stdout io.Writer) error {
					enc := json.NewEncoder(stdout)
					enc.SetIndent("", "  ")
					doc, err := self.SchemaDocument()
					if err != nil {
						return err
					}
					return enc.Encode(doc)
				},
				Output: clicontract.Output{Mode: clicontract.OutputRaw, SuccessExitCodes: []int{0}},
			},
		},
	}
	return self
}
