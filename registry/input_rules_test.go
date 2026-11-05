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

// TestManifestInputRulesRejectSubjectless ties the manifest to the declaration
// rule that an input rule names its subject, since Manifest is callable on an
// app that never went through Tree.
func TestManifestInputRulesRejectSubjectless(t *testing.T) {
	app := inputRulesApp()
	app.Commands[0].InputRules = []*clicontract.Rule{
		clicontract.Requires(clicontract.Present("file"), "a subjectless rule"),
	}
	_, err := Manifest(app, Options{Binary: "testcli"})
	if err == nil || !strings.Contains(err.Error(), "has no field") {
		t.Fatalf("err = %v, want the subjectless rule refused", err)
	}
}
