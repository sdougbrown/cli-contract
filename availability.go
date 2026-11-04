package clicontract

// Availability declarations: each command may gate its availability with
// Umpire rules over declared inputs the consumer supplies at load time
// (profile mode, permission level, feature flags). The declarations render
// into a registry manifest's top-level availability block; see the registry
// package and the cli-registry manifest spec for the consumer side.

// Availability condition types, matching the Umpire v1 condition types.
const (
	ConditionBoolean = "boolean"
	ConditionString  = "string"
	ConditionNumber  = "number"
	ConditionStrings = "string[]"
	ConditionNumbers = "number[]"
)

// Condition declares one availability input the consumer supplies at load.
type Condition struct {
	Name string
	// Type is one of the Condition* constants.
	Type string
	// Description documents the input for consumers.
	Description string
}

// AvailabilityExpr is a condition expression inside an availability rule.
// Build expressions with When, CondEq, CondIn, And, Or, and Not; the zero
// value is not valid.
type AvailabilityExpr struct {
	// Op is one of "cond", "condEq", "condIn", "and", "or", "not".
	Op string `json:"op"`
	// Condition names a declared Condition (cond, condEq, condIn).
	Condition string `json:"condition,omitempty"`
	// Value is the comparison value (condEq).
	Value any `json:"value,omitempty"`
	// Values are the membership values (condIn).
	Values []any `json:"values,omitempty"`
	// Exprs are the operands (and, or).
	Exprs []*AvailabilityExpr `json:"exprs,omitempty"`
	// Expr is the operand (not).
	Expr *AvailabilityExpr `json:"expr,omitempty"`
}

// AvailabilityRule gates a command's availability. Build rules with Gate,
// GateOn, Requires, RequiresOn, Disables, AnyOf, and EitherOf. A rule
// declared on a command applies to that command's tool unless the rule names
// an explicit Field.
type AvailabilityRule struct {
	// Type is one of "enabledWhen", "requires", "disables", "anyOf",
	// "eitherOf".
	Type string `json:"type"`
	// Field is the tool the rule gates; empty means the declaring command.
	Field string `json:"field,omitempty"`
	// Targets are the tools a disables rule turns off.
	Targets []string `json:"targets,omitempty"`
	// When is the rule's condition expression (every type but anyOf).
	When *AvailabilityExpr `json:"when,omitempty"`
	// Reason is reported when the rule fails. Each type carries a default.
	Reason string `json:"reason,omitempty"`
	// Rules are the operands (anyOf).
	Rules []*AvailabilityRule `json:"rules,omitempty"`
	// Branches are the operands (eitherOf).
	Branches map[string][]*AvailabilityRule `json:"branches,omitempty"`
}

// When references a declared boolean condition.
func When(condition string) *AvailabilityExpr {
	return &AvailabilityExpr{Op: "cond", Condition: condition}
}

// CondEq compares a condition against a value.
func CondEq(condition string, value any) *AvailabilityExpr {
	return &AvailabilityExpr{Op: "condEq", Condition: condition, Value: value}
}

// CondIn tests condition membership.
func CondIn(condition string, values ...any) *AvailabilityExpr {
	return &AvailabilityExpr{Op: "condIn", Condition: condition, Values: values}
}

// And requires every operand to pass.
func And(exprs ...*AvailabilityExpr) *AvailabilityExpr {
	return &AvailabilityExpr{Op: "and", Exprs: exprs}
}

// Or passes when any operand passes.
func Or(exprs ...*AvailabilityExpr) *AvailabilityExpr {
	return &AvailabilityExpr{Op: "or", Exprs: exprs}
}

// Not negates its operand.
func Not(expr *AvailabilityExpr) *AvailabilityExpr {
	return &AvailabilityExpr{Op: "not", Expr: expr}
}

// Gate enables the declaring command's tool only when the expression passes.
// The default reason is "condition not met".
func Gate(when *AvailabilityExpr, reason string) *AvailabilityRule {
	return gateRule("", when, reason)
}

// GateOn is Gate targeting an explicitly named tool.
func GateOn(field string, when *AvailabilityExpr, reason string) *AvailabilityRule {
	return gateRule(field, when, reason)
}

// Requires keeps the declaring command's tool gated until the condition is
// met. The default reason is "required condition not met".
func Requires(when *AvailabilityExpr, reason string) *AvailabilityRule {
	return gateRule("", when, reason)
}

// RequiresOn is Requires targeting an explicitly named tool.
func RequiresOn(field string, when *AvailabilityExpr, reason string) *AvailabilityRule {
	return gateRule(field, when, reason)
}

func gateRule(field string, when *AvailabilityExpr, reason string) *AvailabilityRule {
	return &AvailabilityRule{Type: "enabledWhen", Field: field, When: when, Reason: reason}
}

// Disables turns the named tools off while the condition holds. The default
// reason is "overridden by condition".
func Disables(targets []string, when *AvailabilityExpr, reason string) *AvailabilityRule {
	return &AvailabilityRule{Type: "disables", Targets: targets, When: when, Reason: reason}
}

// AnyOf passes when any operand rule passes; all operands must target the
// same tool (an empty field on every operand takes the declaring command's).
func AnyOf(rules ...*AvailabilityRule) *AvailabilityRule {
	return &AvailabilityRule{Type: "anyOf", Rules: rules}
}

// EitherOf passes when any branch's rules all pass.
func EitherOf(group string, branches map[string][]*AvailabilityRule) *AvailabilityRule {
	return &AvailabilityRule{Type: "eitherOf", Branches: branches}
}

// validateAvailability checks the app's availability declarations: known
// expression ops, known rule types, declared conditions referenced, and
// eitherOf group names present.
func validateAvailability(app *App) error {
	declared := make(map[string]bool, len(app.AvailabilityConditions))
	seenCondition := make(map[string]bool, len(app.AvailabilityConditions))
	for _, c := range app.AvailabilityConditions {
		if c.Name == "" {
			return UsageErrorf("availability condition must declare a name")
		}
		if seenCondition[c.Name] {
			return UsageErrorf("availability condition %q is declared more than once", c.Name)
		}
		seenCondition[c.Name] = true
		switch c.Type {
		case ConditionBoolean, ConditionString, ConditionNumber, ConditionStrings, ConditionNumbers:
		default:
			return UsageErrorf(
				"availability condition %q has unsupported type %q", c.Name, c.Type,
			)
		}
		declared[c.Name] = true
	}

	var walk func(path string, cmd *Command) error
	walk = func(path string, cmd *Command) error {
		if len(cmd.Availability) > 0 && len(cmd.Subcommands) > 0 {
			return UsageErrorf(
				"command %q declares availability rules but owns subcommands; only leaf commands run as tools",
				path,
			)
		}
		for i, rule := range cmd.Availability {
			if err := validateAvailabilityRule(path, i, rule, declared); err != nil {
				return err
			}
		}
		for _, sub := range cmd.Subcommands {
			if err := walk(path+" "+sub.Name, sub); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cmd := range app.Commands {
		if err := walk(cmd.Name, cmd); err != nil {
			return err
		}
	}
	return nil
}

func validateAvailabilityRule(path string, index int, rule *AvailabilityRule, declared map[string]bool) error {
	if rule == nil {
		return UsageErrorf("command %q availability rule %d is nil", path, index)
	}
	switch rule.Type {
	case "enabledWhen", "requires", "disables":
		if rule.When == nil {
			return UsageErrorf(
				"command %q availability rule %d (%s) requires a condition expression",
				path, index, rule.Type,
			)
		}
		if err := validateAvailabilityExpr(path, rule.When, declared); err != nil {
			return err
		}
	case "anyOf":
		if len(rule.Rules) == 0 {
			return UsageErrorf("command %q availability rule %d (anyOf) has no rules", path, index)
		}
		for i, inner := range rule.Rules {
			if err := validateAvailabilityRule(path, i, inner, declared); err != nil {
				return err
			}
		}
	case "eitherOf":
		if len(rule.Branches) == 0 {
			return UsageErrorf("command %q availability rule %d (eitherOf) has no branches", path, index)
		}
		for name, branch := range rule.Branches {
			if name == "" {
				return UsageErrorf(
					"command %q availability rule %d (eitherOf) has an unnamed branch", path, index,
				)
			}
			if len(branch) == 0 {
				return UsageErrorf(
					"command %q availability rule %d (eitherOf) branch %q is empty", path, index, name,
				)
			}
			for i, inner := range branch {
				if err := validateAvailabilityRule(path, i, inner, declared); err != nil {
					return err
				}
			}
		}
	default:
		return UsageErrorf(
			"command %q availability rule %d has unknown type %q", path, index, rule.Type,
		)
	}
	return nil
}

func validateAvailabilityExpr(path string, expr *AvailabilityExpr, declared map[string]bool) error {
	if expr == nil {
		return UsageErrorf("command %q availability expression is nil", path)
	}
	switch expr.Op {
	case "cond", "condEq", "condIn":
		if expr.Condition == "" {
			return UsageErrorf(
				"command %q availability %q expression has no condition", path, expr.Op,
			)
		}
		if !declared[expr.Condition] {
			return UsageErrorf(
				"command %q availability expression references undeclared condition %q",
				path, expr.Condition,
			)
		}
	case "and", "or":
		if len(expr.Exprs) == 0 {
			return UsageErrorf(
				"command %q availability %q expression has no operands", path, expr.Op,
			)
		}
		for _, inner := range expr.Exprs {
			if err := validateAvailabilityExpr(path, inner, declared); err != nil {
				return err
			}
		}
	case "not":
		if expr.Expr == nil {
			return UsageErrorf("command %q availability not expression has no operand", path)
		}
		return validateAvailabilityExpr(path, expr.Expr, declared)
	default:
		return UsageErrorf(
			"command %q availability expression has unknown op %q", path, expr.Op,
		)
	}
	return nil
}
