package clicontract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"strings"
	"testing"
	"time"
)

// durationMSApp declares one leaf command whose duration flag publishes as
// whole-number milliseconds: the caller spells an integer, the handler binds
// a time.Duration, and the rules compare JSON numbers.
func durationMSApp() *App {
	return &App{
		Name:      "t",
		Version:   "v1",
		RootShort: "test cli",
		Commands: []*Command{{
			Name:  "wait",
			Short: "wait for something",
			Flags: []Flag{
				{Name: "wait", Kind: DurationMS, Usage: "how long, in milliseconds"},
			},
			InputRules: []*Rule{
				RequiresOn("wait", Eq("wait", 1500), "the wait is 1.5s"),
			},
			Handle: func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
				return map[string]any{"status": "ok"}, nil
			},
		}},
	}
}

// TestDurationMSParsesMilliseconds pins the parser path: the flag binds as a
// whole-number milliseconds spelling into a time.Duration, and the parser
// rejects a duration-string spelling it does not publish.
func TestDurationMSParsesMilliseconds(t *testing.T) {
	app := durationMSApp()
	inv, err := Parse(app, []string{"wait", "--wait", "1500"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := inv.Durations["wait"]; got != 1500*time.Millisecond {
		t.Errorf("Durations[wait] = %v, want 1.5s", got)
	}
	if !inv.Supplied["wait"] {
		t.Error("Supplied[wait] = false, want true")
	}

	// The spelling the contract does not publish is a caller error.
	adapter := &testAdapter{}
	if code, stdout, _ := runApp(t, app, adapter, "wait", "--wait", "1.5s"); code != 2 {
		t.Errorf("exit code = %d, want 2 for a duration-string spelling (%s)", code, stdout)
	}

	// A declared default applies and renders in milliseconds.
	app.Commands[0].Flags[0].Default = 2 * time.Second
	inv, err = Parse(app, []string{"wait"})
	if err != nil {
		t.Fatalf("parse with default: %v", err)
	}
	if got := inv.Durations["wait"]; got != 2*time.Second {
		t.Errorf("default Durations[wait] = %v, want 2s", got)
	}
}

// TestDurationMSEnforcesNumericRules pins enforcement: the rule compares the
// milliseconds number the caller supplied, so a violating invocation is a
// caller error carrying the rule's reason.
func TestDurationMSEnforcesNumericRules(t *testing.T) {
	app := durationMSApp()
	adapter := &testAdapter{}
	if code, _, _ := runApp(t, app, adapter, "wait", "--wait", "1500"); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	adapter = &testAdapter{}
	if code, stdout, _ := runApp(t, app, adapter, "wait", "--wait", "1499"); code != 2 || !strings.Contains(stdout, "the wait is 1.5s") {
		t.Errorf("exit code = %d stdout = %q, want the rule reason at the usage exit", code, stdout)
	}
}

// TestDurationMSSchemaRendersAndRoundTrips pins the published surface: the
// document carries kind "duration_ms" with a whole-number default, and an app
// rebuilt from the document parses the same spellings.
func TestDurationMSSchemaRendersAndRoundTrips(t *testing.T) {
	app := durationMSApp()
	app.Commands[0].Flags[0].Default = 2 * time.Second
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatalf("schema document: %v", err)
	}
	flag := doc.Commands["wait"].Flags["wait"]
	if flag.Kind != "duration_ms" {
		t.Errorf("published kind = %q, want \"duration_ms\"", flag.Kind)
	}
	if flag.Default != float64(2000) && flag.Default != int64(2000) && flag.Default != 2000 {
		t.Errorf("published default = %#v, want 2000", flag.Default)
	}

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseSchemaDocument(data)
	if err != nil {
		t.Fatalf("parse schema document: %v", err)
	}
	rebuilt, err := parsed.App(map[string]HandleFunc{"wait": func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
		return map[string]any{"status": "ok"}, nil
	}})
	if err != nil {
		t.Fatalf("rebuild app: %v", err)
	}
	inv, err := Parse(rebuilt, []string{"wait", "--wait", "1500"})
	if err != nil {
		t.Fatalf("rebuilt parse: %v", err)
	}
	if got := inv.Durations["wait"]; got != 1500*time.Millisecond {
		t.Errorf("rebuilt Durations[wait] = %v, want 1.5s", got)
	}

	// A default that does not match the kind fails the rebuild, not a run.
	badDoc := doc
	badDoc.Commands = maps.Clone(doc.Commands)
	bad := badDoc.Commands["wait"]
	badFlag := bad.Flags["wait"]
	badFlag.Default = "2s"
	bad.Flags["wait"] = badFlag
	badDoc.Commands["wait"] = bad
	broken, _ := json.Marshal(badDoc)
	if parsed, err := ParseSchemaDocument(broken); err == nil {
		handlers := map[string]HandleFunc{"wait": func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
			return map[string]any{"status": "ok"}, nil
		}}
		if _, err := parsed.App(handlers); err == nil {
			t.Error("rebuilt an app whose duration_ms default is a duration string")
		}
	}
}

// TestDurationMSRuleValuesAreNumbers pins the declaration-side check: a
// comparison value on a duration_ms flag must be a whole number of
// milliseconds — a duration-string spelling or a fraction is a declaration
// defect no invocation can satisfy.
func TestDurationMSRuleValuesAreNumbers(t *testing.T) {
	cases := []struct {
		name  string
		expr  *Expr
		want  string
		valid bool
	}{
		{"whole number eq", Eq("wait", 1500), "", true},
		{"whole number membership", In("wait", 1500, 3000), "", true},
		{"duration string value", Eq("wait", "1.5s"), "a duration_ms comparison value is a whole number of milliseconds", false},
		{"fractional value", Eq("wait", 1.5), "a whole number of milliseconds", false},
		{"fractional membership", NotIn("wait", 1.5), "a whole number of milliseconds", false},
		{"nested composite", And(Present("wait"), Eq("wait", "1.5s")), "a duration_ms comparison value", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := durationMSApp()
			app.Commands[0].InputRules = []*Rule{RequiresOn("wait", tc.expr, "reason")}
			if tc.valid {
				app.Tree()
				return
			}
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("declaration accepted an unsatisfiable rule value %#v", tc.expr.Value)
				}
				if msg := fmt.Sprint(r); !strings.Contains(msg, tc.want) {
					t.Errorf("error %q does not include %q", msg, tc.want)
				}
			}()
			app.Tree()
		})
	}
}

// TestConsumerEvaluatesDurationMSRuleLikeTheParser evaluates the emitted
// rules over raw input — no normalization exists to apply, which is the
// point of the kind — and requires the parser's verdicts.
func TestConsumerEvaluatesDurationMSRuleLikeTheParser(t *testing.T) {
	app := durationMSApp()
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
		{"matching milliseconds", map[string]any{"wait": float64(1500)}, true, ""},
		{"other milliseconds", map[string]any{"wait": float64(1499)}, false, "the wait is 1.5s"},
		{"flag omitted", map[string]any{}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := engine.Check(tc.input, nil, nil)
			if err != nil {
				t.Fatalf("evaluation: %v", err)
			}
			// The spec's supplied-only semantics: a rule with a field is a
			// violation only when that flag is supplied, so the consumer
			// reads Umpire's status through the flags its input carries.
			_, supplied := tc.input["wait"]
			availability := status["wait"]
			// A rule with a field is a violation only when the flag is
			// supplied; otherwise Umpire's disabled status is not reported.
			violation := supplied && !availability.Enabled
			if violation {
				if tc.legal {
					t.Errorf("unexpected violation (reason %q)", availability.Reason)
				} else if availability.Reason != tc.reason {
					t.Errorf("reason = %q, want %q", availability.Reason, tc.reason)
				}
			} else if !tc.legal {
				t.Errorf("no violation reported, want %q", tc.reason)
			}
		})
	}
}

// TestDurationMSDocumentSatisfiesTheSpec runs the rendered document through
// the published JSON Schema — the check that keeps the kind's vocabulary
// from drifting between the renderer and the spec.
func TestDurationMSDocumentSatisfiesTheSpec(t *testing.T) {
	doc, err := durationMSApp().SchemaDocument()
	if err != nil {
		t.Fatalf("schema document: %v", err)
	}
	if err := validateDocument(t, doc); err != nil {
		t.Errorf("rendered duration_ms document fails the published schema: %v", err)
	}
}

// TestDurationMSBounds pin the overflow bound: a milliseconds value past
// MaxInt64 nanoseconds cannot become a time.Duration, so the declaration and
// the parser reject it rather than wrap.
func TestDurationMSBounds(t *testing.T) {
	const maxMS = int64(math.MaxInt64) / int64(time.Millisecond)

	// A default past the bound fails the rebuild.
	app := durationMSApp()
	app.Commands[0].Flags[0].Default = time.Duration(maxMS) * time.Millisecond / 2 // in range
	if _, err := app.SchemaDocument(); err != nil {
		t.Fatalf("in-range default: %v", err)
	}
	huge := 1.0e13 // past MaxInt64/1e6 ≈ 9.2e12
	bad := durationMSApp()
	bad.Commands[0].Flags[0].Default = huge
	data, err := bad.SchemaDocument()
	if err != nil {
		t.Fatalf("schema document: %v", err)
	}
	parsed, err := ParseSchemaDocument(mustJSON(t, data))
	if err != nil {
		t.Fatalf("parse schema document: %v", err)
	}
	handlers := map[string]HandleFunc{"wait": func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
		return map[string]any{"status": "ok"}, nil
	}}
	if _, err := parsed.App(handlers); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("rebuild with a %v ms default: %v, want out of range", huge, err)
	}

	// A parsed value past the bound is a caller error, not a wrapped duration.
	adapter := &testAdapter{}
	if code, stdout, _ := runApp(t, durationMSApp(), adapter, "wait", "--wait", "10000000000000"); code != 2 || !strings.Contains(stdout, "out of range") {
		t.Errorf("exit = %d stdout = %q, want an out-of-range usage error", code, stdout)
	}

	// A rule value past the bound is a declaration defect.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("declaration accepted a rule value the parser can never bind")
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, "within") {
			t.Errorf("error %q does not name the bound", msg)
		}
	}()
	unsatisfiable := durationMSApp()
	unsatisfiable.Commands[0].InputRules = []*Rule{RequiresOn("wait", Eq("wait", 1.0e13), "reason")}
	unsatisfiable.Tree()
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
