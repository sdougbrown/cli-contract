package clicontract

import (
	"context"
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
				return nil, nil
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
			RequiresOn("prompt-file", Absent("prompt"), "cannot combine"),
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

// TestInputRulesNameInheritedFlags pins the flag surface an input rule
// validates against: a subcommand's rule may name a flag inherited from an
// ancestor's persistent set. A regression that omitted inherited flags from
// the surface would reject a valid declaration.
func TestInputRulesNameInheritedFlags(t *testing.T) {
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
	app.Tree()

	app.Commands[0].Subcommands[0].InputRules = []*Rule{
		RequiresOn("force", Present("sockett"), "a typo"),
	}
	expectDeclarationPanic(t, app, `references unknown flag "sockett"`)
}
