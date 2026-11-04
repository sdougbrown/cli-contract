package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/sdougbrown/cli-contract"
	"github.com/umpire-tools/umpire-go"
)

func noopHandle(ctx context.Context, inv clicontract.Invocation, stderr io.Writer) (any, error) {
	return map[string]any{}, nil
}

// availabilityApp declares a small CLI with availability rules: a tool that
// gates on a boolean condition, one that requires membership in a declared
// string condition, and a cross-entry disables.
func availabilityApp() *clicontract.App {
	return &clicontract.App{
		Name:      "testcli",
		Version:   "vtest",
		RootShort: "test cli",
		AvailabilityConditions: []clicontract.Condition{
			{Name: "read_only", Type: clicontract.ConditionBoolean, Description: "the profile is read-only"},
			{Name: "region", Type: clicontract.ConditionString, Description: "deployment region"},
		},
		Commands: []*clicontract.Command{
			{Name: "view", Short: "view a thing", Handle: noopHandle},
			{
				Name:   "edit",
				Short:  "edit a thing",
				Handle: noopHandle,
				Availability: []*clicontract.AvailabilityRule{
					clicontract.Gate(clicontract.Not(clicontract.When("read_only")), "the profile is read-only"),
				},
			},
			{
				Name:   "deploy",
				Short:  "deploy to a region",
				Handle: noopHandle,
				Availability: []*clicontract.AvailabilityRule{
					clicontract.Requires(clicontract.CondIn("region", "us", "eu"), "region is not served"),
					clicontract.Disables([]string{"view"}, clicontract.When("read_only"), ""),
				},
			},
		},
	}
}

// decodeAvailability extracts the generated manifest's availability block
// plus its tool names.
func decodeAvailability(t *testing.T, manifest []byte) (map[string]any, []string) {
	t.Helper()
	var m struct {
		Tools        map[string]json.RawMessage `json:"tools"`
		Availability struct {
			Conditions map[string]any `json:"conditions"`
			Rules      []any          `json:"rules"`
		} `json:"availability"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	var tools []string
	for name := range m.Tools {
		tools = append(tools, name)
	}
	block := map[string]any{
		"conditions": m.Availability.Conditions,
		"rules":      m.Availability.Rules,
	}
	return block, tools
}

// compileAvailability validates the emitted block against the real Umpire
// runtime and compiles it to an engine with fields = tool names. This is the
// drift check: the declaration surface cannot emit a block the manifest
// contract rejects.
func compileAvailability(t *testing.T, manifest []byte) *umpire.Umpire {
	t.Helper()
	block, tools := decodeAvailability(t, manifest)
	fields := map[string]any{}
	for _, name := range tools {
		fields[name] = map[string]any{}
	}
	block["version"] = float64(1)
	block["fields"] = fields

	schema, err := umpire.SchemaFromObject(block)
	if err != nil {
		t.Fatalf("generated availability block fails the Umpire contract: %v", err)
	}
	engine, err := umpire.NewFromSchema(schema)
	if err != nil {
		t.Fatalf("compiled availability engine: %v", err)
	}
	return engine
}

func evaluateAvailability(t *testing.T, manifest []byte, inputs map[string]any) map[string]umpire.FieldAvailability {
	t.Helper()
	engine := compileAvailability(t, manifest)
	status, err := engine.Check(map[string]any{}, inputs, nil)
	if err != nil {
		t.Fatalf("availability evaluation: %v", err)
	}
	return status
}

func TestManifestAvailabilityBlock(t *testing.T) {
	manifest, err := Manifest(availabilityApp(), Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}

	// The block passes the Umpire contract and gates as declared.
	status := evaluateAvailability(t, manifest, map[string]any{"read_only": false, "region": "us"})
	if !status["testcli-edit"].Enabled {
		t.Errorf("edit should be available: %+v", status["testcli-edit"])
	}
	if !status["testcli-deploy"].Enabled {
		t.Errorf("deploy should be available in us: %+v", status["testcli-deploy"])
	}

	// Read-only profile: edit gates off, deploy's disables turns view off.
	status = evaluateAvailability(t, manifest, map[string]any{"read_only": true, "region": "us"})
	if status["testcli-edit"].Enabled {
		t.Errorf("edit should be unavailable on a read-only profile")
	}
	if status["testcli-edit"].Reason != "the profile is read-only" {
		t.Errorf("edit reason = %q", status["testcli-edit"].Reason)
	}
	if status["testcli-view"].Enabled {
		t.Errorf("view should be disabled by deploy's rule")
	}
	if status["testcli-view"].Reason != "overridden by condition" {
		t.Errorf("view reason = %q", status["testcli-view"].Reason)
	}

	// Unserved region: deploy gates off with its reason.
	status = evaluateAvailability(t, manifest, map[string]any{"read_only": false, "region": "ap"})
	if status["testcli-deploy"].Enabled {
		t.Errorf("deploy should be unavailable in ap")
	}
	if status["testcli-deploy"].Reason != "region is not served" {
		t.Errorf("deploy reason = %q", status["testcli-deploy"].Reason)
	}
}

func TestManifestAvailabilityDeterministic(t *testing.T) {
	opts := Options{Binary: "testcli"}
	first, err := Manifest(availabilityApp(), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Manifest(availabilityApp(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("manifest with availability is not byte-identical across runs")
	}
}

func TestManifestWithoutAvailabilityOmitsBlock(t *testing.T) {
	manifest, err := Manifest(testApp(), Options{Binary: "testcli"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["availability"]; has {
		t.Error("manifest without availability rules should omit the block")
	}
}

// expectTreePanic builds the cobra tree, which validates the declaration at
// startup. mustFail selects between the failure and the success assertion.
func expectTreePanic(t *testing.T, app *clicontract.App, wantErr string, mustFail bool) {
	t.Helper()
	defer func() {
		r := recover()
		if !mustFail {
			if r != nil {
				t.Fatalf("valid app failed validation: %v", r)
			}
			return
		}
		if r == nil {
			t.Fatalf("expected a validation failure")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, wantErr) {
			t.Errorf("panic %q does not include %q", msg, wantErr)
		}
	}()
	app.Tree()
}

func TestAvailabilityDeclarationValidation(t *testing.T) {
	expectTreePanic(t, availabilityApp(), "", false)

	cases := []struct {
		name    string
		mutate  func(*clicontract.App)
		wantErr string
	}{
		{
			name: "undeclared condition",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					clicontract.Gate(clicontract.When("typo"), ""),
				}
			},
			wantErr: `undeclared condition "typo"`,
		},
		{
			name: "unknown expression op",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					clicontract.Gate(&clicontract.AvailabilityExpr{Op: "eq", Condition: "read_only"}, ""),
				}
			},
			wantErr: `unknown op "eq"`,
		},
		{
			name: "condition without a name",
			mutate: func(a *clicontract.App) {
				a.AvailabilityConditions = append(a.AvailabilityConditions,
					clicontract.Condition{Name: "", Type: clicontract.ConditionBoolean})
			},
			wantErr: "must declare a name",
		},
		{
			name: "unsupported condition type",
			mutate: func(a *clicontract.App) {
				a.AvailabilityConditions[0].Type = "object"
			},
			wantErr: `unsupported type "object"`,
		},
		{
			name: "rule on a parent command",
			mutate: func(a *clicontract.App) {
				a.Commands[0].Subcommands = []*clicontract.Command{
					{Name: "sub", Short: "s", Handle: noopHandle},
				}
				a.Commands[0].Availability = []*clicontract.AvailabilityRule{
					clicontract.Gate(clicontract.When("read_only"), ""),
				}
			},
			wantErr: "owns subcommands",
		},
		{
			name: "empty eitherOf branch",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					clicontract.EitherOf("paths", map[string][]*clicontract.AvailabilityRule{
						"empty": {},
					}),
				}
			},
			wantErr: `branch "empty" is empty`,
		},
		{
			name: "unknown rule type",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					{Type: "oneOf"},
				}
			},
			wantErr: `unknown type "oneOf"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := availabilityApp()
			tc.mutate(app)
			expectTreePanic(t, app, tc.wantErr, true)
		})
	}
}
