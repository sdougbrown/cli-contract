package clicontract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// inputRulesApp declares one leaf command with the flags the input-rule tests
// relate to one another.
func inputRulesApp() *App {
	return &App{
		Name:      "t",
		Version:   "v1",
		RootShort: "test cli",
		Commands: []*Command{{
			Name:  "run",
			Short: "run one thing",
			Flags: []Flag{
				{Name: "roster-file", Kind: String, Usage: "roster file path"},
				{Name: "roster-entry", Kind: String, Usage: "roster entry name"},
				{Name: "prompt", Kind: String, Usage: "prompt text"},
				{Name: "prompt-file", Kind: String, Usage: "prompt file path"},
			},
			Handle: func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
				return map[string]any{"status": "ok"}, nil
			},
		}},
	}
}

func expectDeclarationPanic(t *testing.T, app *App, want string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("Tree did not reject the declaration; want %q", want)
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, want) {
			t.Errorf("panic %q does not include %q", msg, want)
		}
	}()
	app.Tree()
}

// TestInputRuleExpressions pins each field-dialect constructor to the op and
// subject the Umpire contract expects, so the declaration surface cannot drift
// from the wire vocabulary.
func TestInputRuleExpressions(t *testing.T) {
	cases := []struct {
		name  string
		expr  *Expr
		op    string
		value any
		vals  []any
	}{
		{name: "present", expr: Present("prompt"), op: OpPresent},
		{name: "absent", expr: Absent("prompt"), op: OpAbsent},
		{name: "truthy", expr: Truthy("prompt"), op: OpTruthy},
		{name: "falsy", expr: Falsy("prompt"), op: OpFalsy},
		{name: "eq", expr: Eq("prompt", "hi"), op: OpEq, value: "hi"},
		{name: "in", expr: In("prompt", "a", "b"), op: OpIn, vals: []any{"a", "b"}},
		{name: "notIn", expr: NotIn("prompt", "a"), op: OpNotIn, vals: []any{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.expr.Field != "prompt" {
				t.Errorf("Field = %q, want the named property", tc.expr.Field)
			}
			if tc.expr.Condition != "" {
				t.Errorf("Condition = %q, want empty in the field dialect", tc.expr.Condition)
			}
			if tc.expr.Op != tc.op {
				t.Errorf("Op = %q, want %q", tc.expr.Op, tc.op)
			}
			if tc.expr.Value != tc.value {
				t.Errorf("Value = %v, want %v", tc.expr.Value, tc.value)
			}
			if !reflect.DeepEqual(tc.expr.Values, tc.vals) {
				t.Errorf("Values = %v, want %v", tc.expr.Values, tc.vals)
			}
		})
	}
}

// TestInputRulesDeclarationValidation covers the failures a declaration writer
// can reach: unknown subjects, the wrong dialect, missing fields, and rules on
// a command that owns subcommands.
func TestInputRulesDeclarationValidation(t *testing.T) {
	// A valid declaration must not panic.
	t.Run("valid", func(t *testing.T) {
		app := inputRulesApp()
		app.Commands[0].InputRules = []*Rule{
			RequiresOn("roster-file", Present("roster-entry"), "a roster file needs an entry"),
			Disables([]string{"prompt"}, Present("prompt-file"), ""),
		}
		app.Tree()
	})

	cases := []struct {
		name   string
		mutate func(*App)
		want   string
	}{
		{
			name: "unknown subject flag",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					RequiresOn("roster-fiel", Present("roster-entry"), ""),
				}
			},
			want: `names unknown flag "roster-fiel"`,
		},
		{
			name: "missing subject flag",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					Requires(Present("roster-entry"), ""),
				}
			},
			want: "input rules name the flag they constrain",
		},
		{
			name: "unknown compared flag",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					RequiresOn("roster-file", Present("roster-entri"), ""),
				}
			},
			want: `references unknown flag "roster-entri"`,
		},
		{
			name: "condition expression in the input set",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					RequiresOn("roster-file", When("profile"), ""),
				}
			},
			want: "compares consumer conditions",
		},
		{
			name: "mixed dialect through a composite",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					RequiresOn("prompt-file", And(Absent("prompt"), When("profile")), ""),
				}
			},
			want: "compares consumer conditions",
		},
		{
			name: "unknown disables target",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					Disables([]string{"promt"}, Present("prompt-file"), ""),
				}
			},
			want: `targets unknown flag "promt"`,
		},
		{
			name: "composite operands name different flags",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					AnyOf(
						GateOn("roster-file", Present("roster-entry"), ""),
						GateOn("prompt", Present("prompt-file"), ""),
					),
				}
			},
			want: "input rule 0 (anyOf) operands target different flags",
		},
		{
			name: "condition expression names a field",
			mutate: func(a *App) {
				a.AvailabilityConditions = []Condition{{Name: "profile", Type: ConditionString}}
				a.Commands[0].Availability = []*Rule{
					Gate(&Expr{Op: OpCond, Condition: "profile", Field: "prompt"}, ""),
				}
			},
			want: "also names a field",
		},
		{
			name: "field expression also names a condition",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					GateOn("prompt", &Expr{Op: OpPresent, Field: "prompt-file", Condition: "profile"}, ""),
				}
			},
			want: "also names a condition",
		},
		{
			name: "rule value cannot marshal",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					RequiresOn("prompt", Eq("prompt-file", make(chan int)), ""),
				}
			},
			want: "input rules do not compile",
		},
		{
			name: "rules form a cycle",
			mutate: func(a *App) {
				a.Commands[0].InputRules = []*Rule{
					RequiresOn("prompt-file", Absent("prompt"), ""),
					Disables([]string{"prompt"}, Present("prompt-file"), ""),
				}
			},
			want: "input rules do not compile",
		},
		{
			name: "rules on a command that owns subcommands",
			mutate: func(a *App) {
				a.Commands[0].Subcommands = []*Command{
					{Name: "sub", Short: "s", Handle: func(context.Context, Invocation, io.Writer) (any, error) {
						return nil, nil
					}},
				}
				a.Commands[0].InputRules = []*Rule{
					RequiresOn("roster-file", Present("roster-entry"), ""),
				}
			},
			want: "owns subcommands",
		},
		{
			name: "field expression in the availability set",
			mutate: func(a *App) {
				a.AvailabilityConditions = []Condition{
					{Name: "profile", Type: ConditionString},
				}
				a.Commands[0].Availability = []*Rule{
					Gate(Present("prompt"), ""),
				}
			},
			want: "compares the command's own inputs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := inputRulesApp()
			tc.mutate(app)
			expectDeclarationPanic(t, app, tc.want)
		})
	}
}

// TestInputRulesRejectInheritedFlags pins the flags an input rule may name: the
// app's common flags and the leaf's own. An ancestor's persistent flag parses on
// the leaf but cannot become a manifest property, because the argv binding has
// no way to place it before the subcommand name.
func TestInputRulesRejectInheritedFlags(t *testing.T) {
	app := &App{
		Name:      "t",
		Version:   "v1",
		RootShort: "test cli",
		Commands: []*Command{{
			Name:            "store",
			Short:           "store commands",
			PersistentFlags: []Flag{{Name: "socket", Kind: String, Usage: "control socket"}},
			Subcommands: []*Command{{
				Name:  "reset",
				Short: "reset the store",
				Flags: []Flag{{Name: "force", Kind: Bool, Usage: "force the reset"}},
				InputRules: []*Rule{
					RequiresOn("force", Present("socket"), "a forced reset needs a socket"),
				},
				Handle: func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
					return map[string]any{"status": "ok"}, nil
				},
			}},
		}},
	}
	expectDeclarationPanic(t, app, `references unknown flag "socket"`)

	// A common flag carries a property, so a rule may name it.
	app.CommonFlags = []Flag{{Name: "socket", Kind: String, Usage: "control socket"}}
	app.Commands[0].PersistentFlags = nil
	app.Tree()
}

// TestSchemaRendersInputRules pins the rules' place and shape in the schema
// document: beside the flag schemas, in the Umpire wire vocabulary.
func TestSchemaRendersInputRules(t *testing.T) {
	app := inputRulesApp()
	app.Commands[0].InputRules = []*Rule{
		RequiresOn("roster-file", Present("roster-entry"), "a roster file needs an entry"),
		RequiresOn("prompt-file", Absent("prompt"), "cannot combine"),
	}

	b, err := json.Marshal(app.Schemas()["run"])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	want := []any{
		map[string]any{
			"type":   "requires",
			"field":  "roster-file",
			"reason": "a roster file needs an entry",
			"when":   map[string]any{"op": "present", "field": "roster-entry"},
		},
		map[string]any{
			"type":   "requires",
			"field":  "prompt-file",
			"reason": "cannot combine",
			"when":   map[string]any{"op": "absent", "field": "prompt"},
		},
	}
	if !reflect.DeepEqual(got["inputRules"], want) {
		t.Errorf("inputRules = %#v, want %#v", got["inputRules"], want)
	}
	if _, ok := got["flags"].(map[string]any); !ok {
		t.Errorf("flags missing from the same entry: %#v", got)
	}

	// A command without rules omits the key rather than rendering an empty
	// list.
	app.Commands[0].InputRules = nil
	b, err = json.Marshal(app.Schemas()["run"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "inputRules") {
		t.Errorf("schema without rules should omit inputRules: %s", b)
	}
}

// enforcingInputRulesApp declares the relationship set the issue's avenor
// examples describe.
func enforcingInputRulesApp() *App {
	app := inputRulesApp()
	app.Commands[0].InputRules = []*Rule{
		RequiresOn("roster-file", Present("roster-entry"), "a roster file needs an entry"),
		RequiresOn("prompt-file", Absent("prompt"), "cannot combine with --prompt"),
	}
	return app
}

// TestEnforceInputRules rejects a violating invocation through both entry
// points, as a caller error the caller can act on.
func TestEnforceInputRules(t *testing.T) {
	cases := []struct {
		name   string
		argv   []string
		code   int
		reason string
	}{
		{
			name: "dependency supplied",
			argv: []string{"run", "--roster-file", "r.json", "--roster-entry", "main"},
			code: 0,
		},
		{
			name:   "dependency missing",
			argv:   []string{"run", "--roster-file", "r.json"},
			code:   2,
			reason: "a roster file needs an entry",
		},
		{
			name: "neither side of the pair supplied",
			argv: []string{"run"},
			code: 0,
		},
		{
			name:   "excluded sibling supplied",
			argv:   []string{"run", "--prompt", "hi", "--prompt-file", "p.txt"},
			code:   2,
			reason: "cannot combine with --prompt",
		},
		{
			name: "excluded sibling supplied alone",
			argv: []string{"run", "--prompt-file", "p.txt"},
			code: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &testAdapter{}
			code, stdout, stderr := runApp(t, enforcingInputRulesApp(), adapter, tc.argv...)
			if code != tc.code {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, tc.code, stderr)
			}
			if tc.reason == "" {
				return
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
				t.Fatalf("stdout is not one JSON value: %v (%q)", err, stdout)
			}
			errObj := envelope["error"].(map[string]any)
			if errObj["code"] != "usage" {
				t.Errorf("error code = %v, want usage", errObj["code"])
			}
			if msg, _ := errObj["message"].(string); !strings.Contains(msg, tc.reason) {
				t.Errorf("message = %q, want it to include %q", msg, tc.reason)
			}
		})
	}

	if _, err := Parse(enforcingInputRulesApp(), []string{"run", "--roster-file", "r.json"}); err == nil {
		t.Error("Parse accepted an invocation the rules reject")
	} else if !strings.Contains(err.Error(), "a roster file needs an entry") {
		t.Errorf("Parse error = %q, want the rule's reason", err)
	}
}

// TestInputRuleIgnoresDeclaredDefaults pins the presence semantics: a default
// registered on the flag set must not satisfy a rule when the caller omitted
// the flag, or every rule naming a defaulted flag would be inert.
func TestInputRuleIgnoresDeclaredDefaults(t *testing.T) {
	app := inputRulesApp()
	for i := range app.Commands[0].Flags {
		if app.Commands[0].Flags[i].Name == "roster-entry" {
			app.Commands[0].Flags[i].Default = "main"
		}
	}
	app.Commands[0].InputRules = []*Rule{
		RequiresOn("roster-file", Present("roster-entry"), "a roster file needs an entry"),
	}

	adapter := &testAdapter{}
	code, stdout, _ := runApp(t, app, adapter, "run", "--roster-file", "r.json")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2: a declared default satisfied the rule (stdout %q)", code, stdout)
	}
}

// TestRequiredFlagEnforced moves a missing required flag from the handler to
// the parser, with the same caller-error shape as a rule violation.
func TestRequiredFlagEnforced(t *testing.T) {
	app := inputRulesApp()
	for i := range app.Commands[0].Flags {
		if app.Commands[0].Flags[i].Name == "prompt" {
			app.Commands[0].Flags[i].Required = true
		}
	}

	adapter := &testAdapter{}
	code, stdout, _ := runApp(t, app, adapter, "run", "--prompt-file", "p.txt")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (stdout %q)", code, stdout)
	}
	if !strings.Contains(stdout, "missing required flag --prompt") {
		t.Errorf("stdout = %q, want the missing flag named", stdout)
	}

	code, _, _ = runApp(t, app, adapter, "run", "--prompt", "hi")
	if code != 0 {
		t.Errorf("a supplied required flag was rejected: exit %d", code)
	}
}

// TestEnforceInputRulesOnRawCommand covers the raw-output path, which captures
// its invocation outside the dispatch RunE.
func TestEnforceInputRulesOnRawCommand(t *testing.T) {
	app := &App{
		Name:      "t",
		Version:   "v1",
		RootShort: "test cli",
		Commands: []*Command{{
			Name:  "export",
			Short: "write the roster",
			Flags: []Flag{
				{Name: "json", Kind: Bool, Usage: "write JSON"},
				{Name: "yaml", Kind: Bool, Usage: "write YAML"},
			},
			InputRules: []*Rule{
				RequiresOn("yaml", Absent("json"), "cannot combine --json and --yaml"),
			},
			Raw: func(ctx context.Context, inv Invocation, stdout io.Writer) error {
				stdout.Write([]byte("{}\n"))
				return nil
			},
		}},
	}

	adapter := &testAdapter{}
	code, _, _ := runApp(t, app, adapter, "export", "--json", "--yaml")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2: a raw command skipped its input rules", code)
	}
}

// TestEnforceInputRulesTypedFlags covers the kind mapping the evaluator reads
// for value comparisons: an Int, a Duration, and a Repeat property each carry
// their parsed value, not a shared or stringified one.
func TestEnforceInputRulesTypedFlags(t *testing.T) {
	app := &App{
		Name:      "t",
		Version:   "v1",
		RootShort: "test cli",
		Commands: []*Command{{
			Name:  "wait",
			Short: "wait for something",
			Flags: []Flag{
				{Name: "count", Kind: Int, Usage: "how many"},
				{Name: "timeout", Kind: Duration, Usage: "how long"},
				{Name: "tag", Kind: Repeat, Usage: "a tag"},
				{Name: "force", Kind: Bool, Usage: "force it"},
			},
			InputRules: []*Rule{
				RequiresOn("force", In("count", 3, 5), "a forced wait takes 3 or 5"),
				RequiresOn("force", Eq("timeout", "5m0s"), "a forced wait takes 5m"),
				RequiresOn("force", Truthy("tag"), "a forced wait needs a tag"),
			},
			Handle: func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
				return map[string]any{"status": "ok"}, nil
			},
		}},
	}

	cases := []struct {
		name string
		argv []string
		code int
	}{
		{"all three match", []string{"wait", "--force", "--count", "3", "--timeout", "5m", "--tag", "prod"}, 0},
		{"int mismatch", []string{"wait", "--force", "--count", "4", "--timeout", "5m", "--tag", "prod"}, 2},
		{"duration mismatch", []string{"wait", "--force", "--count", "3", "--timeout", "6m", "--tag", "prod"}, 2},
		{"repeat missing", []string{"wait", "--force", "--count", "3", "--timeout", "5m"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &testAdapter{}
			if code, stdout, _ := runApp(t, app, adapter, tc.argv...); code != tc.code {
				t.Errorf("exit code = %d, want %d (%s)", code, tc.code, stdout)
			}
		})
	}
}

// TestEnforceDisables rejects a supplied flag a disables rule turned off.
func TestEnforceDisables(t *testing.T) {
	app := inputRulesApp()
	app.Commands[0].InputRules = []*Rule{
		Disables([]string{"prompt"}, Present("prompt-file"), "prompt is overridden by --prompt-file"),
	}

	adapter := &testAdapter{}
	if code, stdout, _ := runApp(t, app, adapter, "run", "--prompt", "hi", "--prompt-file", "p.txt"); code != 2 {
		t.Errorf("exit code = %d, want 2 (%s)", code, stdout)
	} else if !strings.Contains(stdout, "overridden by --prompt-file") {
		t.Errorf("stdout = %q, want the disables reason", stdout)
	}
	if code, _, _ := runApp(t, app, adapter, "run", "--prompt-file", "p.txt"); code != 0 {
		t.Errorf("exit code = %d, want 0: the source flag alone is legal", code)
	}
}

// TestEnforceRequiredCommonFlag pins the required-flag check over the whole
// surface, not just the leaf's own flags.
func TestEnforceRequiredCommonFlag(t *testing.T) {
	app := inputRulesApp()
	app.CommonFlags = []Flag{{Name: "token", Kind: String, Usage: "auth token", Required: true}}

	adapter := &testAdapter{}
	if code, _, _ := runApp(t, app, adapter, "run"); code != 2 {
		t.Errorf("exit code = %d, want 2: a missing required common flag passed", code)
	}
	if code, _, _ := runApp(t, app, adapter, "run", "--token", "t"); code != 0 {
		t.Errorf("exit code = %d, want 0: a supplied required common flag was rejected", code)
	}
}

// TestEnforceNamedPath keeps the error prefix consistent with every other
// dispatch error: the full command path, top-level name included.
func TestEnforceNamedPath(t *testing.T) {
	app := &App{
		Name:      "t",
		Version:   "v1",
		RootShort: "test cli",
		Commands: []*Command{{
			Name:  "store",
			Short: "store commands",
			Subcommands: []*Command{{
				Name:  "reset",
				Short: "reset the store",
				Flags: []Flag{{Name: "force", Kind: Bool, Usage: "force it", Required: true}},
				Handle: func(ctx context.Context, inv Invocation, stderr io.Writer) (any, error) {
					return map[string]any{"status": "ok"}, nil
				},
			}},
		}},
	}

	adapter := &testAdapter{}
	code, stdout, _ := runApp(t, app, adapter, "store", "reset")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (%s)", code, stdout)
	}
	if !strings.Contains(stdout, "store reset: missing required flag --force") {
		t.Errorf("stdout = %q, want the full command path", stdout)
	}
}

// TestInputRulesGivenTogether pins the form for a pair that must appear
// together: each flag gates on the other. Expressing it as two requires rules
// is a dependency cycle, which the declaration validation rejects.
func TestInputRulesGivenTogether(t *testing.T) {
	app := inputRulesApp()
	app.Commands[0].InputRules = []*Rule{
		GateOn("roster-file", Present("roster-entry"), "a roster file needs an entry"),
		GateOn("roster-entry", Present("roster-file"), "an entry needs a roster file"),
	}

	cases := []struct {
		name   string
		argv   []string
		code   int
		reason string
	}{
		{"both supplied", []string{"run", "--roster-file", "r.json", "--roster-entry", "main"}, 0, ""},
		{"neither supplied", []string{"run"}, 0, ""},
		{"file alone", []string{"run", "--roster-file", "r.json"}, 2, "a roster file needs an entry"},
		{"entry alone", []string{"run", "--roster-entry", "main"}, 2, "an entry needs a roster file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &testAdapter{}
			code, stdout, _ := runApp(t, app, adapter, tc.argv...)
			if code != tc.code {
				t.Fatalf("exit code = %d, want %d (%s)", code, tc.code, stdout)
			}
			if tc.reason != "" && !strings.Contains(stdout, tc.reason) {
				t.Errorf("stdout = %q, want %q", stdout, tc.reason)
			}
		})
	}
}
