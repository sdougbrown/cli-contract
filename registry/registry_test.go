package registry

import (
	"encoding/json"
	"testing"

	"github.com/sdougbrown/cli-contract"
)

func testApp() *clicontract.App {
	return &clicontract.App{
		Name:    "testcli",
		Version: "vtest",
		Commands: []*clicontract.Command{
			{
				Name:        "reconcile",
				Short:       "collect feedback and establish the baseline",
				Positionals: []clicontract.Positional{{Name: "URL", Required: true}},
				Flags: []clicontract.Flag{
					{Name: "head", Kind: clicontract.String, Usage: "expected head SHA", Required: true},
				},
			},
			{
				Name:        "ack",
				Short:       "acknowledge delivered event IDs",
				Positionals: []clicontract.Positional{{Name: "URL", Required: true}},
				Flags: []clicontract.Flag{
					{Name: "consumer", Kind: clicontract.String, Usage: "consumer name", Required: true},
					{Name: "event", Kind: clicontract.Repeat, Usage: "event ID"},
				},
			},
			{
				Name:  "store",
				Short: "store utilities",
				Subcommands: []*clicontract.Command{
					{Name: "path", Short: "print the store path"},
					{Name: "reset", Short: "delete the store", Flags: []clicontract.Flag{
						{Name: "target", Kind: clicontract.String, Usage: "scope the reset"},
					}},
				},
			},
		},
		CommonFlags: []clicontract.Flag{
			{Name: "state-dir", Kind: clicontract.String, Usage: "override the state directory"},
		},
	}
}

func TestManifestShape(t *testing.T) {
	b, err := Manifest(testApp(), Options{
		Binary:      "testcli",
		Timeouts:    map[string]string{"reconcile": "5m"},
		CommonFlags: []string{"state-dir"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["version"] != float64(1) {
		t.Errorf("version = %v", m["version"])
	}
	tools := m["tools"].(map[string]any)
	for _, name := range []string{"reconcile", "ack", "testcli-store-path", "testcli-store-reset"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("manifest missing tool %q", name)
		}
	}
	reconcile := tools["reconcile"].(map[string]any)
	if reconcile["description"] != "collect feedback and establish the baseline" {
		t.Errorf("description = %v", reconcile["description"])
	}
	if reconcile["timeout"] != "5m" {
		t.Errorf("timeout = %v, want the override", reconcile["timeout"])
	}
	schema := reconcile["inputSchema"].(map[string]any)
	req := schema["required"].([]any)
	if len(req) != 2 || req[0] != "URL" || req[1] != "head" {
		t.Errorf("required = %v, want URL and head (sorted)", req)
	}
	props := schema["properties"].(map[string]any)
	head := props["head"].(map[string]any)
	if head["type"] != "string" || head["required"] != true {
		t.Errorf("head property = %v, want string+required", head)
	}
	ack := tools["ack"].(map[string]any)
	aschema := ack["inputSchema"].(map[string]any)
	event := aschema["properties"].(map[string]any)["event"].(map[string]any)
	if event["type"] != "array" {
		t.Errorf("event type = %v, want array", event["type"])
	}
	abindings := ack["bindings"].([]any)
	var eventStyle string
	for _, b := range abindings {
		row := b.(map[string]any)
		if row["property"] == "event" {
			eventStyle = row["style"].(string)
		}
	}
	if eventStyle != "equals" {
		t.Errorf("event binding style = %q, want equals", eventStyle)
	}
	// The tool's command is the binary plus the declared path.
	if cmd := ack["command"].([]any); cmd[0] != "testcli" || cmd[1] != "ack" {
		t.Errorf("command = %v", cmd)
	}
}

func TestManifestDeterministic(t *testing.T) {
	opts := Options{Binary: "testcli", CommonFlags: []string{"state-dir"}}
	first, err := Manifest(testApp(), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Manifest(testApp(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("manifest output is not byte-identical across runs")
	}
}

func TestManifestBindingRows(t *testing.T) {
	// Binding rows carry property, style, and flag; positional rows have no
	// flag, and every row's style is one of the documented values.
	b, err := Manifest(testApp(), Options{Binary: "testcli", CommonFlags: []string{"state-dir"}})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tools map[string]struct {
			Bindings []struct {
				Property string `json:"property"`
				Style    string `json:"style"`
				Flag     string `json:"flag"`
			} `json:"bindings"`
			Output struct {
				Mode             string `json:"mode"`
				SuccessExitCodes []int  `json:"success_exit_codes"`
			} `json:"output"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for tool, t2 := range m.Tools {
		for _, row := range t2.Bindings {
			switch row.Style {
			case "positional":
				if row.Flag != "" {
					t.Errorf("%s: positional binding %q carries flag %q", tool, row.Property, row.Flag)
				}
			case "space", "equals":
				if row.Flag == "" {
					t.Errorf("%s: binding %q has no flag", tool, row.Property)
				}
			default:
				t.Errorf("%s: binding %q has unknown style %q", tool, row.Property, row.Style)
			}
		}
		if t2.Output.Mode != "json" {
			t.Errorf("%s: output mode = %q, want json", tool, t2.Output.Mode)
		}
	}
}
