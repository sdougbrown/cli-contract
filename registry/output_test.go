package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sdougbrown/cli-contract"
)

// outputOf renders the manifest for one app and returns one tool's output
// block.
func outputOf(t *testing.T, app *clicontract.App, tool string) map[string]any {
	t.Helper()
	b, err := Manifest(app, Options{Binary: "testcli"})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tools map[string]struct {
			Output map[string]any `json:"output"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	entry, ok := m.Tools[tool]
	if !ok {
		t.Fatalf("manifest has no tool %q", tool)
	}
	return entry.Output
}

func TestManifestOutputFollowsDeclaration(t *testing.T) {
	app := &clicontract.App{
		Name: "testcli",
		Commands: []*clicontract.Command{
			{
				Name: "plain", Handle: noopHandle,
				Short:  "one JSON value",
				Output: clicontract.Output{Mode: clicontract.OutputJSON, SuccessExitCodes: []int{0}},
			},
			{
				Name: "field", Handle: noopHandle,
				Short:  "one JSON value, one field",
				Output: clicontract.Output{Mode: clicontract.OutputJSON, JSONPointer: "/status", SuccessExitCodes: []int{0}},
			},
			{
				Name: "text", Handle: noopHandle,
				Short:  "raw text",
				Output: clicontract.Output{Mode: clicontract.OutputRaw, SuccessExitCodes: []int{0}},
			},
			{
				Name: "quiet", Handle: noopHandle,
				Short:  "no output",
				Output: clicontract.Output{Mode: clicontract.OutputNone, SuccessExitCodes: []int{0}},
			},
			{
				Name: "undeclared", Handle: noopHandle,
				Short: "keeps the bridge default",
			},
		},
	}

	plain := outputOf(t, app, "testcli-plain")
	if plain["mode"] != "json" {
		t.Errorf("plain mode = %v, want json", plain["mode"])
	}
	// The manifest publishes exactly the codes the declaration states.
	if codes, ok := plain["success_exit_codes"].([]any); !ok || len(codes) != 1 || codes[0] != float64(0) {
		t.Errorf("plain success codes = %v, want [0]", plain["success_exit_codes"])
	}
	if _, ok := plain["json_pointer"]; ok {
		t.Errorf("plain json_pointer = %v, want absent for a pointer-less declaration", plain["json_pointer"])
	}

	field := outputOf(t, app, "testcli-field")
	if field["mode"] != "json-pointer" || field["json_pointer"] != "/status" {
		t.Errorf("field output = %v, want json-pointer at /status", field)
	}

	if got := outputOf(t, app, "testcli-text")["mode"]; got != "raw" {
		t.Errorf("text mode = %v, want raw", got)
	}
	quiet := outputOf(t, app, "testcli-quiet")
	if quiet["mode"] != "raw" {
		t.Errorf("quiet mode = %v, want raw: there is nothing to decode", quiet["mode"])
	}
	if codes, ok := quiet["success_exit_codes"].([]any); !ok || len(codes) != 1 || codes[0] != float64(0) {
		t.Errorf("quiet success codes = %v, want [0]", quiet["success_exit_codes"])
	}

	undeclared := outputOf(t, app, "testcli-undeclared")
	if undeclared["mode"] != "json" {
		t.Errorf("undeclared mode = %v, want the envelope default", undeclared["mode"])
	}
	codes, ok := undeclared["success_exit_codes"].([]any)
	if !ok || !reflect.DeepEqual(codes, []any{float64(0), float64(1), float64(2), float64(3), float64(4)}) {
		t.Errorf("undeclared success codes = %v, want the envelope default [0 1 2 3 4]", undeclared["success_exit_codes"])
	}
}

// TestManifestRequiresRunnableCommands pins the shared-walk guarantee: the
// manifest renders a declaration that can run, so a leaf without exactly one
// of Handle/Raw/Stream is rejected like the tree rejects it.
func TestManifestRequiresRunnableCommands(t *testing.T) {
	app := &clicontract.App{
		Name: "testcli",
		Commands: []*clicontract.Command{
			{Name: "ghost", Short: "a command that can never run"},
		},
	}
	_, err := Manifest(app, Options{Binary: "testcli"})
	if err == nil || !strings.Contains(err.Error(), "must declare exactly one of Handle, Raw, or Stream") {
		t.Fatalf("err = %v, want the handler-less rejection", err)
	}
}

// TestManifestRejectsMalformedOutput pins the direct entry point: Manifest
// renders the declaration without building a tree, so it must run the same
// output check Run and Tree run.
func TestManifestRejectsMalformedOutput(t *testing.T) {
	app := &clicontract.App{
		Name: "testcli",
		Commands: []*clicontract.Command{
			{Name: "bad", Handle: noopHandle, Short: "bad output", Output: clicontract.Output{Mode: "yaml"}},
		},
	}
	_, err := Manifest(app, Options{Binary: "testcli"})
	if err == nil || !strings.Contains(err.Error(), `output mode "yaml"`) {
		t.Fatalf("err = %v, want the output mode rejection", err)
	}

	parent := &clicontract.App{
		Name: "testcli",
		Commands: []*clicontract.Command{
			{
				Name:   "store",
				Short:  "store utilities",
				Output: clicontract.Output{Mode: clicontract.OutputJSON},
				Subcommands: []*clicontract.Command{
					{Name: "path", Short: "print the path", Handle: noopHandle},
				},
			},
		},
	}
	_, err = Manifest(parent, Options{Binary: "testcli"})
	if err == nil || !strings.Contains(err.Error(), "only a leaf command can") {
		t.Fatalf("err = %v, want the non-leaf output rejection", err)
	}

	codesless := &clicontract.App{
		Name: "testcli",
		Commands: []*clicontract.Command{
			{Name: "bad", Short: "bad output", Handle: noopHandle, Output: clicontract.Output{Mode: clicontract.OutputJSON}},
		},
	}
	_, err = Manifest(codesless, Options{Binary: "testcli"})
	if err == nil || !strings.Contains(err.Error(), "without success exit codes") {
		t.Fatalf("err = %v, want the missing success codes rejection", err)
	}
}
