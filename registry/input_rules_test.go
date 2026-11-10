package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sdougbrown/cli-contract"
	"github.com/umpire-tools/umpire-go"
)

// inputRulesApp declares one leaf command whose flags relate to one another.
func inputRulesApp() *clicontract.App {
	return &clicontract.App{
		Name:      "testcli",
		Version:   "vtest",
		RootShort: "test cli",
		CommonFlags: []clicontract.Flag{
			{Name: "verbose", Kind: clicontract.Bool, Usage: "log verbosely"},
		},
		Commands: []*clicontract.Command{{
			Name:   "run",
			Short:  "run a thing",
			Handle: noopHandle,
			Flags: []clicontract.Flag{
				{Name: "file", Kind: clicontract.String, Usage: "roster file path"},
				{Name: "entry", Kind: clicontract.String, Usage: "roster entry name"},
				{Name: "prompt", Kind: clicontract.String, Usage: "prompt text"},
				{Name: "prompt-file", Kind: clicontract.String, Usage: "prompt file path"},
			},
			InputRules: []*clicontract.Rule{
				clicontract.RequiresOn("file", clicontract.Present("entry"), "a roster file needs an entry"),
				clicontract.RequiresOn("prompt-file", clicontract.Absent("prompt"), "cannot combine with --prompt"),
			},
		}},
	}
}

// decodeTool returns one tool's declared properties and input rules.
func decodeTool(t *testing.T, manifest []byte, tool string) (map[string]any, []any) {
	t.Helper()
	var m struct {
		Tools map[string]struct {
			InputSchema struct {
				Properties map[string]any `json:"properties"`
			} `json:"inputSchema"`
			InputRules []any `json:"inputRules"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	body, ok := m.Tools[tool]
	if !ok {
		t.Fatalf("manifest has no tool %q", tool)
	}
	return body.InputSchema.Properties, body.InputRules
}

// compileInputRules validates the emitted block against the real Umpire
// runtime, with fields derived from the tool's own input schema. This is the
// drift check: the declaration surface cannot emit a block the manifest
// contract rejects.
func compileInputRules(t *testing.T, manifest []byte, tool string) *umpire.Umpire {
	t.Helper()
	props, rules := decodeTool(t, manifest, tool)
	fields := make(map[string]any, len(props))
	for name := range props {
		fields[name] = map[string]any{}
	}
	block := map[string]any{"version": float64(1), "fields": fields, "rules": rules}

	schema, err := umpire.SchemaFromObject(block)
	if err != nil {
		t.Fatalf("generated input rules fail the Umpire contract: %v", err)
	}
	engine, err := umpire.NewFromSchema(schema)
	if err != nil {
		t.Fatalf("compiled input-rule engine: %v", err)
	}
	return engine
}

func TestManifestRendersInputRules(t *testing.T) {
	manifest, err := Manifest(inputRulesApp(), Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}

	_, rules := decodeTool(t, manifest, "testcli-run")
	want := []any{
		map[string]any{
			"type":   "requires",
			"field":  "file",
			"reason": "a roster file needs an entry",
			"when":   map[string]any{"op": "present", "field": "entry"},
		},
		map[string]any{
			"type":   "requires",
			"field":  "prompt-file",
			"reason": "cannot combine with --prompt",
			"when":   map[string]any{"op": "absent", "field": "prompt"},
		},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("inputRules = %#v, want %#v", rules, want)
	}

	// A tool without rules omits the key.
	app := inputRulesApp()
	app.Commands[0].InputRules = nil
	manifest, err = Manifest(app, Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest without rules: %v", err)
	}
	if strings.Contains(string(manifest), "inputRules") {
		t.Errorf("manifest without input rules should omit the block: %s", manifest)
	}
}

// TestManifestInputRulesGating compiles the emitted block and asserts the
// gating behavior the consumer relies on.
func TestManifestInputRulesGating(t *testing.T) {
	manifest, err := Manifest(inputRulesApp(), Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	engine := compileInputRules(t, manifest, "testcli-run")

	cases := []struct {
		name   string
		values map[string]any
		field  string
		want   bool
		reason string
	}{
		{
			name:   "dependency missing",
			values: map[string]any{"file": "r.json"},
			field:  "file",
			want:   false,
			reason: "a roster file needs an entry",
		},
		{
			name:   "dependency supplied",
			values: map[string]any{"file": "r.json", "entry": "main"},
			field:  "file",
			want:   true,
		},
		{
			name:   "excluded flag supplied alongside its sibling",
			values: map[string]any{"prompt": "hi", "prompt-file": "p.txt"},
			field:  "prompt-file",
			want:   false,
			reason: "cannot combine with --prompt",
		},
		{
			name:   "excluded flag supplied alone",
			values: map[string]any{"prompt-file": "p.txt"},
			field:  "prompt-file",
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := engine.Check(tc.values, nil, nil)
			if err != nil {
				t.Fatalf("evaluation: %v", err)
			}
			got := status[tc.field]
			if got.Enabled != tc.want {
				t.Errorf("%s enabled = %v, want %v (reason %q)", tc.field, got.Enabled, tc.want, got.Reason)
			}
			if !tc.want && tc.reason != "" && got.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.reason)
			}
		})
	}
}

// TestManifestInputRulesUnexposedSubject fails when a rule names a common flag
// the manifest does not expose, rather than emitting a dangling reference.
func TestManifestInputRulesUnexposedSubject(t *testing.T) {
	app := inputRulesApp()
	app.Commands[0].InputRules = append(app.Commands[0].InputRules,
		clicontract.RequiresOn("file", clicontract.Present("verbose"), "verbose needs a file"),
	)
	_, err := Manifest(app, Options{Binary: "testcli"})
	if err == nil {
		t.Fatal("expected an error for a rule naming an unexposed common flag")
	}
	for _, want := range []string{`"verbose"`, "does not expose as a property"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not include %q", err, want)
		}
	}
}

// TestSchemaAndManifestAgreeOnInputRules keeps the two renderings of one
// declaration from drifting: the schema document and the manifest must carry
// the same rules for the same command.
func TestSchemaAndManifestAgreeOnInputRules(t *testing.T) {
	app := inputRulesApp()
	app.Tree()
	manifest, err := Manifest(app, Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}

	_, fromManifest := decodeTool(t, manifest, "testcli-run")
	schemaJSON, err := json.Marshal(app.Schemas()["run"].InputRules)
	if err != nil {
		t.Fatal(err)
	}
	var fromSchema []any
	if err := json.Unmarshal(schemaJSON, &fromSchema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromSchema, fromManifest) {
		t.Errorf("schema rules = %#v\nmanifest rules = %#v", fromSchema, fromManifest)
	}
}

// TestManifestInputRulesRejectParent mirrors the startup check: a command that
// owns subcommands runs as no tool, so its rules have nowhere to render.
func TestManifestInputRulesRejectParent(t *testing.T) {
	app := inputRulesApp()
	app.Commands[0].Subcommands = []*clicontract.Command{{
		Name: "sub", Short: "a subcommand", Handle: noopHandle,
	}}
	_, err := Manifest(app, Options{Binary: "testcli"})
	if err == nil || !strings.Contains(err.Error(), "owns subcommands") {
		t.Fatalf("err = %v, want the parent-command refusal", err)
	}
}

// TestManifestInputRulesUnexposedSubjectIsTheSubject covers the other half of
// the property check: the rule's own field, not just the flags its expression
// compares.
func TestManifestInputRulesUnexposedSubjectIsTheSubject(t *testing.T) {
	app := inputRulesApp()
	app.Commands[0].InputRules = []*clicontract.Rule{
		clicontract.RequiresOn("verbose", clicontract.Present("file"), "a verbose rule"),
	}
	_, err := Manifest(app, Options{Binary: "testcli"})
	if err == nil || !strings.Contains(err.Error(), `"verbose"`) {
		t.Fatalf("err = %v, want the unexposed subject named", err)
	}
}

// invocationRequirementApp mirrors the avenor verify case: one non-empty config
// source is required on every invocation.
func invocationRequirementApp() *clicontract.App {
	return &clicontract.App{
		Name:      "testcli",
		Version:   "vtest",
		RootShort: "test cli",
		Commands: []*clicontract.Command{{
			Name:   "verify",
			Short:  "verify a roster",
			Handle: noopHandle,
			Flags: []clicontract.Flag{
				{Name: "loop-file", Kind: clicontract.String, Usage: "loop file path"},
				{Name: "team-file", Kind: clicontract.String, Usage: "team file path"},
				{Name: "roster-file", Kind: clicontract.String, Usage: "roster file path"},
			},
			InputRules: []*clicontract.Rule{
				clicontract.Requires(
					clicontract.Or(
						clicontract.Truthy("loop-file"),
						clicontract.Truthy("team-file"),
						clicontract.Truthy("roster-file"),
					),
					"one of --loop-file, --team-file, or --roster-file is required",
				),
			},
		}},
	}
}

const invocationRequirementReason = "one of --loop-file, --team-file, or --roster-file is required"

// TestManifestRendersInvocationRequirement pins the fieldless rule on the
// manifest wire: it keeps its type, condition, and reason but carries no field,
// so a consumer can tell it constrains the invocation as a whole.
func TestManifestRendersInvocationRequirement(t *testing.T) {
	manifest, err := Manifest(invocationRequirementApp(), Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	_, rules := decodeTool(t, manifest, "testcli-verify")
	if len(rules) != 1 {
		t.Fatalf("inputRules = %#v, want the declared requirement", rules)
	}
	rule := rules[0].(map[string]any)
	if _, ok := rule["field"]; ok {
		t.Errorf("invocation rule carries a field: %#v", rule)
	}
	if rule["type"] != "requires" {
		t.Errorf("type = %v, want requires", rule["type"])
	}
	if rule["reason"] != invocationRequirementReason {
		t.Errorf("reason = %v, want the declared reason", rule["reason"])
	}
}

// TestManifestInputRulesUnexposedInvocationFlag fails a fieldless requirement
// whose expression names a common flag the manifest does not expose.
func TestManifestInputRulesUnexposedInvocationFlag(t *testing.T) {
	app := inputRulesApp()
	app.Commands[0].InputRules = []*clicontract.Rule{
		clicontract.Requires(clicontract.Truthy("verbose"), "verbose is required"),
	}
	_, err := Manifest(app, Options{Binary: "testcli"})
	if err == nil || !strings.Contains(err.Error(), `"verbose"`) {
		t.Fatalf("err = %v, want the unexposed flag named", err)
	}
}

// TestManifestInvocationRequirementGating compiles the fieldless rule the way a
// consumer must, on a synthetic field, and checks the Umpire expression
// distinguishes absent from explicitly empty values.
func TestManifestInvocationRequirementGating(t *testing.T) {
	const invocationField = "$invocation"
	manifest, err := Manifest(invocationRequirementApp(), Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	props, rules := decodeTool(t, manifest, "testcli-verify")
	fields := make(map[string]any, len(props)+1)
	for name := range props {
		fields[name] = map[string]any{}
	}
	fields[invocationField] = map[string]any{}

	var pin func(map[string]any)
	pin = func(rule map[string]any) {
		if rule["type"] == "requires" || rule["type"] == "enabledWhen" {
			if _, ok := rule["field"]; !ok {
				rule["field"] = invocationField
			}
		}
		if inner, ok := rule["rules"].([]any); ok {
			for _, r := range inner {
				pin(r.(map[string]any))
			}
		}
		if branches, ok := rule["branches"].(map[string]any); ok {
			for _, branch := range branches {
				for _, r := range branch.([]any) {
					pin(r.(map[string]any))
				}
			}
		}
	}
	for _, rule := range rules {
		pin(rule.(map[string]any))
	}

	schema, err := umpire.SchemaFromObject(map[string]any{"version": float64(1), "fields": fields, "rules": rules})
	if err != nil {
		t.Fatalf("generated invocation rule fails the Umpire contract: %v", err)
	}
	engine, err := umpire.NewFromSchema(schema)
	if err != nil {
		t.Fatalf("compiled invocation-rule engine: %v", err)
	}

	cases := []struct {
		name   string
		values map[string]any
		want   bool
	}{
		{name: "no values", values: map[string]any{}, want: false},
		{name: "explicitly empty", values: map[string]any{"loop-file": ""}, want: false},
		{name: "one non-empty", values: map[string]any{"team-file": "foo"}, want: true},
		{name: "all empty", values: map[string]any{"loop-file": "", "team-file": ""}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := engine.Check(tc.values, nil, nil)
			if err != nil {
				t.Fatalf("evaluation: %v", err)
			}
			got := status[invocationField]
			if got.Enabled != tc.want {
				t.Errorf("invocation enabled = %v, want %v (reason %q)", got.Enabled, tc.want, got.Reason)
			}
			if !tc.want && got.Reason != invocationRequirementReason {
				t.Errorf("reason = %q, want %q", got.Reason, invocationRequirementReason)
			}
		})
	}
}

// TestManifestRequiredCommonFlag keeps the manifest's required list in step
// with the parse-time check: a common flag the tool exposes as required is
// declared required in the schema too.
func TestManifestRequiredCommonFlag(t *testing.T) {
	app := inputRulesApp()
	app.CommonFlags = []clicontract.Flag{
		{Name: "token", Kind: clicontract.String, Usage: "auth token", Required: true},
	}
	manifest, err := Manifest(app, Options{Binary: "testcli", CommonFlags: []string{"token"}})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var m struct {
		Tools map[string]struct {
			InputSchema struct {
				Required []string `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	required := m.Tools["testcli-run"].InputSchema.Required
	found := false
	for _, name := range required {
		if name == "token" {
			found = true
		}
	}
	if !found {
		t.Errorf("required = %v, want the required common flag listed", required)
	}
}

// TestManifestMarksDurationInputs renders a duration flag into the tool's
// input schema and checks the marker a consumer normalizes from: the
// property is a string constrained by format go-duration, and the emitted
// rules evaluate raw input spelled 0m exactly as the CLI classifies it once
// the consumer applies that normalization.
func TestManifestMarksDurationInputs(t *testing.T) {
	app := &clicontract.App{
		Name:      "testcli",
		Version:   "vtest",
		RootShort: "test cli",
		CommonFlags: []clicontract.Flag{
			{Name: "timeout", Kind: clicontract.Duration, Usage: "how long"},
		},
		Commands: []*clicontract.Command{{
			Name:   "wait",
			Short:  "wait for something",
			Handle: noopHandle,
			Flags: []clicontract.Flag{
				{Name: "loop", Kind: clicontract.String, Usage: "loop target"},
				{Name: "wait", Kind: clicontract.Duration, Usage: "how long"},
			},
			InputRules: []*clicontract.Rule{
				clicontract.RequiresOn("wait", clicontract.Eq("wait", "0s"), "looping requires --wait=0s"),
			},
		}},
	}
	manifest, err := Manifest(app, Options{Binary: "testcli", CommonFlags: []string{"timeout"}})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	props, _ := decodeTool(t, manifest, "testcli-wait")
	if want := map[string]any{"type": "string", "format": "go-duration"}; !reflect.DeepEqual(props["wait"], want) {
		t.Errorf("--wait property = %#v, want %#v", props["wait"], want)
	}
	if want := map[string]any{"type": "string", "format": "go-duration"}; !reflect.DeepEqual(props["timeout"], want) {
		t.Errorf("common --timeout property = %#v, want %#v", props["timeout"], want)
	}
	if want := map[string]any{"type": "string"}; !reflect.DeepEqual(props["loop"], want) {
		t.Errorf("--loop property = %#v, want %#v: only duration flags carry the marker", props["loop"], want)
	}

	// The consumer path: compile the emitted rules, normalize the raw input
	// per the marker, and require the CLI's verdicts.
	engine := compileInputRules(t, manifest, "testcli-wait")
	cases := []struct {
		name   string
		values map[string]any
		legal  bool
		reason string
	}{
		{"zero spelled 0m, normalized", map[string]any{"loop": "x", "wait": "0s"}, true, ""},
		{"non-zero", map[string]any{"loop": "x", "wait": "1m0s"}, false, "looping requires --wait=0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := engine.Check(tc.values, nil, nil)
			if err != nil {
				t.Fatalf("evaluation: %v", err)
			}
			availability := status["wait"]
			if availability.Enabled != tc.legal {
				t.Errorf("enabled = %v (reason %q), want %v", availability.Enabled, availability.Reason, tc.legal)
			}
			if !tc.legal && availability.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", availability.Reason, tc.reason)
			}
		})
	}
}

// TestManifestInputSchemaOneKindMapping pins the collapsed kind→property
// mapping: a declared kind renders the same property shape whether the flag
// belongs to the command or is a common flag, so the manifest cannot publish
// two shapes for one kind.
func TestManifestInputSchemaOneKindMapping(t *testing.T) {
	app := &clicontract.App{
		Name:      "testcli",
		Version:   "vtest",
		RootShort: "test cli",
		CommonFlags: []clicontract.Flag{
			{Name: "tag", Kind: clicontract.Repeat, Usage: "a common tag"},
			{Name: "limit", Kind: clicontract.Int, Usage: "a common limit"},
		},
		Commands: []*clicontract.Command{{
			Name:   "run",
			Short:  "run a thing",
			Handle: noopHandle,
			Flags: []clicontract.Flag{
				{Name: "count", Kind: clicontract.Int, Usage: "how many"},
				{Name: "names", Kind: clicontract.Repeat, Usage: "some names"},
				{Name: "force", Kind: clicontract.Bool, Usage: "force it"},
				{Name: "wait", Kind: clicontract.Duration, Usage: "how long"},
			},
		}},
	}
	manifest, err := Manifest(app, Options{Binary: "testcli", CommonFlags: []string{"tag", "limit"}})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	props, _ := decodeTool(t, manifest, "testcli-run")
	want := map[string]map[string]any{
		"count": {"type": "integer"},
		"names": {"type": "array", "items": map[string]any{"type": "string"}},
		"force": {"type": "boolean"},
		"wait":  {"type": "string", "format": "go-duration"},
		"tag":   {"type": "array", "items": map[string]any{"type": "string"}},
		"limit": {"type": "integer"},
	}
	for name, shape := range want {
		if !reflect.DeepEqual(props[name], shape) {
			t.Errorf("--%s property = %#v, want %#v", name, props[name], shape)
		}
	}
}

// TestManifestMarksDurationMSInputs pins the duration_ms surface: the
// property is an integer carrying the unit marker, so a consumer knows the
// value is whole milliseconds without reading the usage string.
func TestManifestMarksDurationMSInputs(t *testing.T) {
	app := &clicontract.App{
		Name:      "testcli",
		Version:   "vtest",
		RootShort: "test cli",
		Commands: []*clicontract.Command{{
			Name:   "wait",
			Short:  "wait for something",
			Handle: noopHandle,
			Flags: []clicontract.Flag{
				{Name: "wait", Kind: clicontract.DurationMS, Usage: "how long, in milliseconds"},
			},
		}},
	}
	manifest, err := Manifest(app, Options{Binary: "testcli"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	props, _ := decodeTool(t, manifest, "testcli-wait")
	want := map[string]any{"type": "integer", "format": "duration-ms"}
	if !reflect.DeepEqual(props["wait"], want) {
		t.Errorf("--wait property = %#v, want %#v", props["wait"], want)
	}
}
