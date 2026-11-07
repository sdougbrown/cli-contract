package clicontract

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// specSchema compiles the published v1 schema document schema.
func specSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.NewCompiler().Compile("spec/cli-contract.schema.json")
	if err != nil {
		t.Fatalf("compile spec schema: %v", err)
	}
	return schema
}

// validateDocument runs one document through the published schema.
func validateDocument(t *testing.T, doc any) error {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	return specSchema(t).Validate(value)
}

// docFactCopy renders one document to its wire shape so a test can layer app
// facts on top of the library's own output.
func docFactCopy(doc SchemaDocument) (map[string]any, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, err
	}
	return wire, nil
}

// TestSchemaDocumentMatchesTheSpec pins the contract between the rendering and
// the published document schema: a document this library emits is a conforming
// v1 document.
func TestSchemaDocumentMatchesTheSpec(t *testing.T) {
	app := schemaDocumentApp()
	// One input rule so the spec's command vocabulary is exercised too: a
	// rendered input_rules member must satisfy the schema's input_rules
	// property, not a stale key from an earlier draft.
	app.Commands[0].InputRules = []*Rule{
		RequiresOn("depth", Present("timeout"), "a depth needs a timeout"),
	}
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDocument(t, doc); err != nil {
		t.Fatalf("the library's own document does not satisfy the spec:\n%v", err)
	}
	// An app with no common flags renders no common_flags member; the spec
	// must accept that shape too, or the library's own output is non-free.
	app.CommonFlags = nil
	doc, err = app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDocument(t, doc); err != nil {
		t.Fatalf("document without common flags does not satisfy the spec:\n%v", err)
	}
	// The four reserved app-fact members are the spec's acceptance promise:
	// a document carrying them validates. Spec drift that drops any of the
	// four from the schema's properties would fail here.
	facts, err := docFactCopy(doc)
	if err != nil {
		t.Fatal(err)
	}
	facts["docs"] = "https://example.test/docs"
	facts["typical_flow"] = []string{"fetch first, then edit"}
	facts["cli_version"] = "1.2.3"
	facts["envelope"] = map[string]any{"ok": true}
	facts["exit_codes"] = map[string]any{"build-failed": 31}
	if err := validateDocument(t, facts); err != nil {
		t.Fatalf("document with reserved app facts does not satisfy the spec:\n%v", err)
	}
	// The handler-owned-signals shape is library-rendered too: signals omitted,
	// handler_owned_signals present. Spec drift that rejects it must fail here.
	app.HandlerOwnedSignals = true
	doc, err = app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDocument(t, doc); err != nil {
		t.Fatalf("handler-owned document does not satisfy the spec:\n%v", err)
	}
}

// TestSpecSchemaRejectsNonConformingDocuments keeps the schema honest: a schema
// that accepts everything would pass the positive test above.
func TestSpecSchemaRejectsNonConformingDocuments(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(doc map[string]any)
	}{
		{
			name: "unknown top-level member",
			mutate: func(doc map[string]any) {
				doc["typicall_flow"] = []any{"fetch"}
			},
		},
		{
			name: "no commands",
			mutate: func(doc map[string]any) {
				doc["commands"] = map[string]any{}
			},
		},
		{
			name: "unknown flag kind",
			mutate: func(doc map[string]any) {
				flag := commandMember(t, doc, "fetch", "flags", "depth")
				flag.(map[string]any)["kind"] = "float"
			},
		},
		{
			// Mutates edit's output (mode "raw", no pointer) so the mode
			// enum rejects it on its own: fetch's pointered output would be
			// rejected by the pointer-forces-json branch regardless.
			name: "unknown output mode",
			mutate: func(doc map[string]any) {
				output := commandMember(t, doc, "edit", "output").(map[string]any)
				output["mode"] = "yaml"
			},
		},
		{
			name: "declared mode without success codes",
			mutate: func(doc map[string]any) {
				output := commandMember(t, doc, "edit", "output").(map[string]any)
				delete(output, "success_exit_codes")
			},
		},
		{
			name: "empty subcommands object",
			mutate: func(doc map[string]any) {
				storeCmd := commandMember(t, doc, "store").(map[string]any)
				storeCmd["subcommands"] = map[string]any{}
			},
		},
		{
			name: "pointer outside json output",
			mutate: func(doc map[string]any) {
				output := commandMember(t, doc, "edit", "output").(map[string]any)
				output["json_pointer"] = "/status"
			},
		},
		{
			// The per-kind default constraints: a duration default is a Go
			// duration string, not a number — the reference apply path rejects
			// what the type split now rejects too.
			name: "duration default carries a number",
			mutate: func(doc map[string]any) {
				flag := commandMember(t, doc, "fetch", "flags", "timeout").(map[string]any)
				flag["default"] = 5
			},
		},
		{
			// multipleOf 1 pins the whole-number rule for integer defaults.
			name: "integer default carries a fraction",
			mutate: func(doc map[string]any) {
				flag := commandMember(t, doc, "fetch", "flags", "depth").(map[string]any)
				flag["default"] = 2.5
			},
		},
		{
			name: "repeat default carries a non-string item",
			mutate: func(doc map[string]any) {
				flag := commandMember(t, doc, "fetch", "flags", "tags").(map[string]any)
				flag["default"] = []any{"draft", 7}
			},
		},
		{
			// The per-kind rule constraints: the schema must reject what the
			// reference apply path rejects (validateRule), so a hand-written
			// rule the validator accepts but the walk would reject cannot
			// masquerade as a conforming document.
			name: "enabledWhen rule without a condition",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "enabledWhen", "field": "depth"})
			},
		},
		{
			name: "requires rule without a condition",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "requires", "field": "depth"})
			},
		},
		{
			name: "disables rule without a condition",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "disables", "targets": []any{"depth"}})
			},
		},
		{
			name: "disables rule without targets",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "disables", "when": map[string]any{"op": "present", "field": "depth"}})
			},
		},
		{
			name: "disables rule with empty targets",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "disables", "when": map[string]any{"op": "present", "field": "depth"}, "targets": []any{}})
			},
		},
		{
			name: "anyOf rule without rules",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "anyOf"})
			},
		},
		{
			name: "anyOf rule with empty rules",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "anyOf", "rules": []any{}})
			},
		},
		{
			name: "eitherOf rule without a group",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "eitherOf", "branches": map[string]any{"mode": []any{map[string]any{"type": "enabledWhen", "when": map[string]any{"op": "truthy", "field": "depth"}}}}})
			},
		},
		{
			name: "eitherOf rule without branches",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "eitherOf", "group": "mode"})
			},
		},
		{
			name: "eitherOf rule with empty branches",
			mutate: func(doc map[string]any) {
				inputRule(t, doc, map[string]any{"type": "eitherOf", "group": "mode", "branches": map[string]any{}})
			},
		},
		{
			// The exit_codes fact is the spec's named-exit-codes surface: the
			// values must stay whole non-negative integers.
			name: "exit codes fact carries a string",
			mutate: func(doc map[string]any) {
				doc["exit_codes"] = map[string]any{"build-failed": "31"}
			},
		},
		{
			name: "exit codes fact carries a negative code",
			mutate: func(doc map[string]any) {
				doc["exit_codes"] = map[string]any{"build-failed": -1}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := schemaDocumentApp().SchemaDocument()
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(rendered)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(b, &doc); err != nil {
				t.Fatal(err)
			}
			tc.mutate(doc)
			if err := validateDocument(t, doc); err == nil {
				t.Error("the spec accepted a non-conforming document")
			}
		})
	}
}

// inputRule installs one hand-written rule as fetch's only input rule, so a
// case can hand the schema a rule the reference walk would reject.
func inputRule(t *testing.T, doc map[string]any, body map[string]any) {
	t.Helper()
	fetch := commandMember(t, doc, "fetch").(map[string]any)
	fetch["input_rules"] = []any{body}
}

// commandMember walks commands.<command>.<key> and returns the value, failing
// the test when the path is absent.
func commandMember(t *testing.T, doc map[string]any, command string, key ...string) any {
	t.Helper()
	commands := doc["commands"].(map[string]any)
	cmd, ok := commands[command].(map[string]any)
	if !ok {
		t.Fatalf("document has no command %q", command)
	}
	var current any = cmd
	for _, k := range key {
		obj, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("commands.%s is not an object", command)
		}
		current, ok = obj[k]
		if !ok {
			t.Fatalf("commands.%s has no %q member", command, k)
		}
	}
	return current
}
