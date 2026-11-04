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

	// The emitted block compiles and gates as declared.
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

func TestManifestAvailabilityCompositeBuilders(t *testing.T) {
	app := &clicontract.App{
		Name:      "testcli",
		Version:   "vtest",
		RootShort: "test cli",
		AvailabilityConditions: []clicontract.Condition{
			{Name: "mode", Type: clicontract.ConditionString},
			{Name: "write_ok", Type: clicontract.ConditionBoolean},
		},
		Commands: []*clicontract.Command{
			{
				Name:   "quick",
				Short:  "quick path",
				Handle: noopHandle,
				// GateOn with an explicit field, plus requires with a
				// compound expression.
				Availability: []*clicontract.AvailabilityRule{
					clicontract.GateOn(
						"testcli-quick",
						clicontract.Or(
							clicontract.CondEq("mode", "full"),
							clicontract.CondEq("mode", "extended"),
						),
						"mode does not allow quick",
					),
					clicontract.RequiresOn(
						"testcli-quick",
						clicontract.And(clicontract.When("write_ok"), clicontract.Not(clicontract.CondEq("mode", "frozen"))),
						"",
					),
				},
			},
			{
				Name:   "either",
				Short:  "either-of paths",
				Handle: noopHandle,
				Availability: []*clicontract.AvailabilityRule{
					clicontract.EitherOf("auth", map[string][]*clicontract.AvailabilityRule{
						"token": {clicontract.Gate(clicontract.CondEq("mode", "full"), "")},
						"sso":   {clicontract.Gate(clicontract.CondEq("mode", "sso"), "")},
					}),
				},
			},
		},
	}
	// AnyOf: operands gate the same tool, one explicit and one inherited.
	app.Commands[0].Availability = append(app.Commands[0].Availability,
		clicontract.AnyOf(
			clicontract.GateOn("testcli-quick", clicontract.CondEq("mode", "full"), ""),
			clicontract.Gate(clicontract.CondEq("mode", "extended"), ""),
		),
	)

	manifest, err := Manifest(app, Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	expectTreePanic(t, app, "", false)

	// Full mode: quick enabled (or + anyOf pass, requires passes).
	status := evaluateAvailability(t, manifest, map[string]any{"mode": "full", "write_ok": true})
	if !status["testcli-quick"].Enabled {
		t.Errorf("quick should be enabled in full mode: %+v", status["testcli-quick"])
	}
	if !status["testcli-either"].Enabled {
		t.Errorf("either should be enabled (token branch matches full): %+v", status["testcli-either"])
	}

	// Basic mode: everything gates off with the composed reasons.
	status = evaluateAvailability(t, manifest, map[string]any{"mode": "basic", "write_ok": true})
	if status["testcli-quick"].Enabled {
		t.Errorf("quick should be gated off in basic mode")
	}
	if status["testcli-either"].Enabled {
		t.Errorf("either should be gated off in basic mode")
	}
	if status["testcli-quick"].Reason != "mode does not allow quick" {
		t.Errorf("quick reason = %q", status["testcli-quick"].Reason)
	}
}

func TestManifestAvailabilityEitherOfGroup(t *testing.T) {
	// EitherOf's group must render into the manifest; the Umpire contract
	// requires it and a dropped group would fail the consumer.
	manifest, err := Manifest(availabilityApp(), Options{Binary: "testcli"})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Availability struct {
			Rules []map[string]any `json:"rules"`
		} `json:"availability"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	for _, rule := range m.Availability.Rules {
		if rule["type"] == "eitherOf" {
			if group, _ := rule["group"].(string); group == "" {
				t.Error("eitherOf rule rendered without a group")
			}
		}
	}
}

func TestManifestAvailabilityUnresolvedTargetFails(t *testing.T) {
	app := availabilityApp()
	app.Commands[2].Availability = append(app.Commands[2].Availability,
		clicontract.Disables([]string{"nonexistent"}, clicontract.When("read_only"), ""))
	if _, err := Manifest(app, Options{Binary: "testcli"}); err == nil {
		t.Fatal("expected Manifest to fail on a target matching no declared command")
	}
}

func TestManifestAvailabilityDoesNotMutateApp(t *testing.T) {
	app := availabilityApp()
	before := fmt.Sprint(app.Commands[2].Availability[1].Targets)
	_, err := Manifest(app, Options{Binary: "testcli"})
	if err != nil {
		t.Fatal(err)
	}
	after := fmt.Sprint(app.Commands[2].Availability[1].Targets)
	if before != after {
		t.Errorf("Manifest mutated the declared targets: %q → %q", before, after)
	}
}

func TestManifestAvailabilityConditionsOnly(t *testing.T) {
	app := &clicontract.App{
		Name:    "testcli",
		Version: "vtest",
		Commands: []*clicontract.Command{
			{Name: "view", Short: "v", Handle: noopHandle},
		},
		AvailabilityConditions: []clicontract.Condition{
			{Name: "on", Type: clicontract.ConditionBoolean},
		},
	}
	manifest, err := Manifest(app, Options{Binary: "testcli"})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Availability struct {
			Conditions map[string]any `json:"conditions"`
			Rules      []any          `json:"rules"`
		} `json:"availability"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m.Availability.Conditions["on"]; !has {
		t.Error("conditions-only app should still render the declared conditions")
	}
	if len(m.Availability.Rules) != 0 {
		t.Errorf("conditions-only app should render no rules: %v", m.Availability.Rules)
	}
	// And the block still passes the consumer contract.
	_ = evaluateAvailability(t, manifest, map[string]any{"on": true})
}

func TestAvailabilityDeclarationValidationMore(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*clicontract.App)
		wantErr string
	}{
		{
			name: "nil when",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					clicontract.Gate(nil, ""),
				}
			},
			wantErr: "requires a condition expression",
		},
		{
			name: "anyOf with no rules",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					clicontract.AnyOf(),
				}
			},
			wantErr: "(anyOf) has no rules",
		},
		{
			name: "and with no operands",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					clicontract.Gate(clicontract.And(), ""),
				}
			},
			wantErr: `"and" expression has no operands`,
		},
		{
			name: "eitherOf without a group",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					{Type: "eitherOf", Branches: map[string][]*clicontract.AvailabilityRule{
						"a": {clicontract.Gate(clicontract.When("read_only"), "")},
					}},
				}
			},
			wantErr: "requires a group name",
		},
		{
			name: "anyOf operands target different tools",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					clicontract.AnyOf(
						clicontract.GateOn("testcli-view", clicontract.When("read_only"), ""),
						clicontract.GateOn("testcli-deploy", clicontract.When("read_only"), ""),
					),
				}
			},
			wantErr: "operands target different tools",
		},
		{
			name: "duplicate condition name",
			mutate: func(a *clicontract.App) {
				a.AvailabilityConditions = append(a.AvailabilityConditions,
					clicontract.Condition{Name: "read_only", Type: clicontract.ConditionString})
			},
			wantErr: `condition "read_only" is declared more than once`,
		},
		{
			name: "eitherOf with no branches",
			mutate: func(a *clicontract.App) {
				a.Commands[1].Availability = []*clicontract.AvailabilityRule{
					clicontract.EitherOf("paths", nil),
				}
			},
			wantErr: "(eitherOf) has no branches",
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

func TestManifestAvailabilityTargetsResolveThroughComposites(t *testing.T) {
	// Disables targets declared inside anyOf rules and eitherOf branches
	// must resolve through resolveTargets' recursion; a regression that
	// skips it would emit dangling tool names the consumer rejects.
	app := availabilityApp()
	app.Commands[2].Availability = append(app.Commands[2].Availability,
		clicontract.AnyOf(
			clicontract.Disables([]string{"view"}, clicontract.When("read_only"), ""),
			clicontract.Gate(clicontract.When("read_only"), ""),
		),
	)
	app.Commands[2].Availability = append(app.Commands[2].Availability,
		clicontract.EitherOf("relay", map[string][]*clicontract.AvailabilityRule{
			"main": {clicontract.Disables([]string{"edit"}, clicontract.When("read_only"), "")},
		}),
	)
	manifest, err := Manifest(app, Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	body := string(manifest)
	if !strings.Contains(body, `"targets": [
        "testcli-view"
      ]`) && !strings.Contains(body, "testcli-view") {
		t.Error("anyOf-wrapped disables target did not resolve to a tool name")
	}
	if !strings.Contains(body, "testcli-edit") {
		t.Error("eitherOf-branch disables target did not resolve to a tool name")
	}
	if strings.Contains(body, `"targets": [
        "view"`) {
		t.Error("a bare command path leaked into the manifest targets")
	}
}

func TestAvailabilityEitherOfBranchFieldMismatch(t *testing.T) {
	// The consumer's composite constructors panic when operands gate
	// different tools; validation must catch it at the declaration site.
	app := availabilityApp()
	app.Commands[1].Availability = []*clicontract.AvailabilityRule{
		clicontract.EitherOf("auth", map[string][]*clicontract.AvailabilityRule{
			"a": {clicontract.GateOn("testcli-view", clicontract.When("read_only"), "")},
			"b": {clicontract.GateOn("testcli-deploy", clicontract.When("read_only"), "")},
		}),
	}
	expectTreePanic(t, app, "operands target different tools", true)
}

func TestManifestAvailabilityMixedOperandMismatchFails(t *testing.T) {
	// An anyOf mixing an explicit GateOn with empty-field operands (which
	// resolve to the declaring tool) spans two tools after resolution.
	// Declaration-time validation sees only one explicit field, so the
	// error must surface in Manifest on the resolved rules.
	app := availabilityApp()
	app.Commands[1].Availability = []*clicontract.AvailabilityRule{
		clicontract.AnyOf(
			clicontract.GateOn("testcli-view", clicontract.When("read_only"), ""),
			clicontract.Gate(clicontract.When("read_only"), ""),
		),
	}
	if _, err := Manifest(app, Options{Binary: "testcli"}); err == nil {
		t.Fatal("expected Manifest to fail on a composite spanning two tools")
	}
}
