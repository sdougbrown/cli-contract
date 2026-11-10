package clicontract

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"strings"
	"testing"
	"time"
)

// durationRulesApp declares one leaf command whose legality turns on a
// duration comparison: looping is legal only when the wait is zero. The
// invocation-wide gate is the issue's repro shape: a caller may spell zero
// as 0m or 0s, and the CLI must accept both spellings alike.
func durationRulesApp() *App {
	return &App{
		Name:      "t",
		Version:   "v1",
		RootShort: "test cli",
		Commands: []*Command{{
			Name:  "wait",
			Short: "wait for something",
			Flags: []Flag{
				{Name: "loop", Kind: String, Usage: "loop target"},
				{Name: "wait", Kind: Duration, Usage: "how long"},
			},
			InputRules: []*Rule{
				Requires(Or(Absent("loop"), Eq("wait", "0s")), "looping requires --wait=0s"),
			},
			Handle: func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
				return map[string]any{"status": "ok"}, nil
			},
		}},
	}
}

// durationRuleCases are the invocations every surface — the declared parser,
// the app rebuilt from the published document, and the spec-conforming
// consumer — must classify alike.
func durationRuleCases() []struct {
	name string
	argv []string
	code int
} {
	return []struct {
		name string
		argv []string
		code int
	}{
		{"zero spelled 0m", []string{"wait", "--loop", "x", "--wait", "0m"}, 0},
		{"zero spelled 0s", []string{"wait", "--loop", "x", "--wait", "0s"}, 0},
		{"non-zero spelling normalized", []string{"wait", "--loop", "x", "--wait", "90s"}, 2},
		{"non-zero spelled canonically", []string{"wait", "--loop", "x", "--wait", "1m30s"}, 2},
		{"loop without wait", []string{"wait", "--loop", "x"}, 2},
		{"no flags", []string{"wait"}, 0},
		{"wait without loop", []string{"wait", "--wait", "5m"}, 0},
	}
}

// TestDurationRuleCanonicalSpelling pins the parser path: rule evaluation
// reads the caller's duration spelling as its canonical Go duration string,
// so equivalent spellings classify alike and an invalid spelling is a parse
// fault rather than a rule violation.
func TestDurationRuleCanonicalSpelling(t *testing.T) {
	app := durationRulesApp()
	for _, tc := range durationRuleCases() {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &testAdapter{}
			if code, stdout, _ := runApp(t, app, adapter, tc.argv...); code != tc.code {
				t.Errorf("exit code = %d, want %d (%s)", code, tc.code, stdout)
			}
		})
	}

	// A non-zero wait fails with the rule's reason.
	adapter := &testAdapter{}
	if code, stdout, _ := runApp(t, app, adapter, "wait", "--loop", "x", "--wait", "1m"); code != 2 || !strings.Contains(stdout, "looping requires") {
		t.Errorf("exit = %d stdout = %q, want the rule reason at the usage exit", code, stdout)
	}

	// An unparseable spelling never reaches rule evaluation: the parser
	// rejects it, with a diagnostic that names the flag.
	adapter = &testAdapter{}
	if code, stdout, _ := runApp(t, app, adapter, "wait", "--loop", "x", "--wait", "xyz"); code != 2 {
		t.Errorf("exit code = %d, want 2 (%s)", code, stdout)
	} else if strings.Contains(stdout, "looping requires") {
		t.Errorf("stdout = %q, want a parse diagnostic, not the rule reason", stdout)
	}
}

// TestDurationRuleAgreesThroughPublishedRule runs the same invocations
// against the app a consumer rebuilds from the published schema document.
// The emitted rule and the parser must classify equivalent spellings alike.
func TestDurationRuleAgreesThroughPublishedRule(t *testing.T) {
	app := durationRulesApp()
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatalf("schema document: %v", err)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseSchemaDocument(data)
	if err != nil {
		t.Fatalf("parse schema document: %v", err)
	}
	cmd := parsed.Commands["wait"]
	if len(cmd.InputRules) != 1 || cmd.InputRules[0].Type != "requires" {
		t.Fatalf("published input rules = %#v, want the invocation-wide gate", cmd.InputRules)
	}
	rebuilt, err := parsed.App(map[string]HandleFunc{"wait": func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
		return map[string]any{"status": "ok"}, nil
	}})
	if err != nil {
		t.Fatalf("rebuild app: %v", err)
	}
	for _, tc := range durationRuleCases() {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &testAdapter{}
			if code, stdout, _ := runApp(t, rebuilt, adapter, tc.argv...); code != tc.code {
				t.Errorf("exit code = %d, want %d (%s)", code, tc.code, stdout)
			}
		})
	}
}

// consumerValues normalizes one invocation's raw input the way the contract
// tells a consumer to: each duration flag's value parses as a Go duration
// string and evaluates as its canonical rendering. The document's flags are
// where the consumer learns which inputs are durations.
func consumerValues(t *testing.T, doc *SchemaDocument, input map[string]any) map[string]any {
	t.Helper()
	out := maps.Clone(input)
	for name, flag := range doc.Commands["wait"].Flags {
		if flag.Kind != "duration" {
			continue
		}
		value, ok := out[name].(string)
		if !ok {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			t.Fatalf("input %q for duration flag --%s: %v", value, name, err)
		}
		out[name] = parsed.String()
	}
	return out
}

// TestConsumerEvaluatesPublishedRuleLikeTheParser evaluates the emitted rule
// with the Umpire runtime over input a consumer normalized per the contract,
// and requires the same verdicts the parser produced. This is the
// agreement the issue asks for: the CLI and a consumer evaluating the
// emitted rules classify equivalent duration spellings alike.
func TestConsumerEvaluatesPublishedRuleLikeTheParser(t *testing.T) {
	app := durationRulesApp()
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatalf("schema document: %v", err)
	}
	cmd := doc.Commands["wait"]
	surface, err := flagsFromSchema("command \"wait\"", cmd.Flags)
	if err != nil {
		t.Fatalf("flags from schema: %v", err)
	}
	rules := make([]*Rule, 0, len(cmd.InputRules))
	for i := range cmd.InputRules {
		rules = append(rules, &cmd.InputRules[i])
	}
	engine, _, err := compileInputRules(surface, rules)
	if err != nil {
		t.Fatalf("compile published rules: %v", err)
	}

	cases := []struct {
		name   string
		input  map[string]any
		legal  bool
		reason string
	}{
		{"zero spelled 0m", map[string]any{"loop": "x", "wait": "0m"}, true, ""},
		{"zero spelled 0s", map[string]any{"loop": "x", "wait": "0s"}, true, ""},
		{"non-zero", map[string]any{"loop": "x", "wait": "1m"}, false, "looping requires --wait=0s"},
		{"loop supplied without wait", map[string]any{"loop": "x"}, false, "looping requires --wait=0s"},
		{"no flags", map[string]any{}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := engine.Check(consumerValues(t, &doc, tc.input), nil, nil)
			if err != nil {
				t.Fatalf("evaluation: %v", err)
			}
			availability := status[invocationField]
			if availability.Enabled != tc.legal {
				t.Errorf("enabled = %v (reason %q), want %v", availability.Enabled, availability.Reason, tc.legal)
			}
			if !tc.legal && availability.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", availability.Reason, tc.reason)
			}
		})
	}
}

// TestDocumentFlagsCarryDurationKind checks the piece of information a
// consumer normalizes from: the published document names the duration kind
// on the flag, and only on the duration flag.
func TestDocumentFlagsCarryDurationKind(t *testing.T) {
	doc, err := durationRulesApp().SchemaDocument()
	if err != nil {
		t.Fatalf("schema document: %v", err)
	}
	kinds := map[string]bool{}
	for name, flag := range doc.Commands["wait"].Flags {
		kinds[name] = flag.Kind == "duration"
	}
	if !kinds["wait"] {
		t.Errorf("--wait publishes kind %q, want \"duration\"", doc.Commands["wait"].Flags["wait"].Kind)
	}
	if kinds["loop"] {
		t.Errorf("--loop publishes as a duration; only duration flags carry the kind")
	}
}
