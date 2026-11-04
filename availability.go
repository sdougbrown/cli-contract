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

// Availability rule types.
const (
	RuleEnabledWhen = "enabledWhen"
	RuleRequires    = "requires"
	RuleDisables    = "disables"
	RuleAnyOf       = "anyOf"
	RuleEitherOf    = "eitherOf"
)

// Availability expression ops.
const (
	OpCond   = "cond"
	OpCondEq = "condEq"
	OpCondIn = "condIn"
	OpAnd    = "and"
	OpOr     = "or"
	OpNot    = "not"
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
	// Composite rules (anyOf, eitherOf) carry no field.
	Field string `json:"field,omitempty"`
	// Targets are the tools a disables rule turns off.
	Targets []string `json:"targets,omitempty"`
	// Group names an eitherOf rule; required for eitherOf.
	Group string `json:"group,omitempty"`
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
	return &AvailabilityExpr{Op: OpCond, Condition: condition}
}

// CondEq compares a condition against a value.
func CondEq(condition string, value any) *AvailabilityExpr {
	return &AvailabilityExpr{Op: OpCondEq, Condition: condition, Value: value}
}

// CondIn tests condition membership.
func CondIn(condition string, values ...any) *AvailabilityExpr {
	return &AvailabilityExpr{Op: OpCondIn, Condition: condition, Values: values}
}

// And requires every operand to pass.
func And(exprs ...*AvailabilityExpr) *AvailabilityExpr {
	return &AvailabilityExpr{Op: OpAnd, Exprs: exprs}
}

// Or passes when any operand passes.
func Or(exprs ...*AvailabilityExpr) *AvailabilityExpr {
	return &AvailabilityExpr{Op: OpOr, Exprs: exprs}
}

// Not negates its operand.
func Not(expr *AvailabilityExpr) *AvailabilityExpr {
	return &AvailabilityExpr{Op: OpNot, Expr: expr}
}

// Gate enables the declaring command's tool only when the expression passes.
// An empty reason takes the default "condition not met".
func Gate(when *AvailabilityExpr, reason string) *AvailabilityRule {
	return gateRule("", RuleEnabledWhen, when, reason, "condition not met")
}

// GateOn is Gate targeting an explicitly named tool.
func GateOn(field string, when *AvailabilityExpr, reason string) *AvailabilityRule {
	return gateRule(field, RuleEnabledWhen, when, reason, "condition not met")
}

// Requires keeps the declaring command's tool gated until the condition is
// met. An empty reason takes the default "required condition not met".
func Requires(when *AvailabilityExpr, reason string) *AvailabilityRule {
	return gateRule("", RuleRequires, when, reason, "required condition not met")
}

// RequiresOn is Requires targeting an explicitly named tool.
func RequiresOn(field string, when *AvailabilityExpr, reason string) *AvailabilityRule {
	return gateRule(field, RuleRequires, when, reason, "required condition not met")
}

func gateRule(field, ruleType string, when *AvailabilityExpr, reason, defaultReason string) *AvailabilityRule {
	if reason == "" {
		reason = defaultReason
	}
	return &AvailabilityRule{Type: ruleType, Field: field, When: when, Reason: reason}
}

// Disables turns the named tools off while the condition holds. An empty
// reason takes the default "overridden by condition".
func Disables(targets []string, when *AvailabilityExpr, reason string) *AvailabilityRule {
	if reason == "" {
		reason = "overridden by condition"
	}
	return &AvailabilityRule{Type: RuleDisables, Targets: targets, When: when, Reason: reason}
}

// AnyOf passes when any operand rule passes; all operands must target the
// same tool (an empty field on every operand takes the declaring command's).
func AnyOf(rules ...*AvailabilityRule) *AvailabilityRule {
	return &AvailabilityRule{Type: RuleAnyOf, Rules: rules}
}

// EitherOf passes when any branch's rules all pass. The group names the
// choice in the rendered manifest and in consumer diagnostics.
func EitherOf(group string, branches map[string][]*AvailabilityRule) *AvailabilityRule {
	return &AvailabilityRule{Type: RuleEitherOf, Group: group, Branches: branches}
}

// validateAvailability checks the app's availability declarations: known
// rule types and expression ops, declared conditions referenced, anyOf and
// eitherOf operands gating one tool, and rules only on leaf commands. It
// returns the condition set and the per-command rules for the shared
// validate walk to apply.
func validateAvailability(app *App) (*availabilityChecks, error) {
	declared := make(map[string]bool, len(app.AvailabilityConditions))
	seenCondition := make(map[string]bool, len(app.AvailabilityConditions))
	for _, c := range app.AvailabilityConditions {
		if c.Name == "" {
			return nil, UsageErrorf("availability condition must declare a name")
		}
		if seenCondition[c.Name] {
			return nil, UsageErrorf("availability condition %q is declared more than once", c.Name)
		}
		seenCondition[c.Name] = true
		switch c.Type {
		case ConditionBoolean, ConditionString, ConditionNumber, ConditionStrings, ConditionNumbers:
		default:
			return nil, UsageErrorf(
				"availability condition %q has unsupported type %q", c.Name, c.Type,
			)
		}
		declared[c.Name] = true
	}

	checks := &availabilityChecks{declared: declared, rulesByCmd: map[*Command][]*AvailabilityRule{}}
	var collect func(cmd *Command) error
	collect = func(cmd *Command) error {
		if len(cmd.Availability) > 0 && len(cmd.Subcommands) > 0 {
			return UsageErrorf(
				"a command that owns subcommands declares availability rules; only leaf commands run as tools",
			)
		}
		if len(cmd.Availability) > 0 {
			checks.rulesByCmd[cmd] = cmd.Availability
		}
		for _, sub := range cmd.Subcommands {
			if err := collect(sub); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cmd := range app.Commands {
		if err := collect(cmd); err != nil {
			return nil, err
		}
	}
	return checks, nil
}

// availabilityChecks carries the validated availability declarations into
// the shared command-tree walk: the declared condition names and the rules
// declared per command (leaf commands only).
type availabilityChecks struct {
	declared   map[string]bool
	rulesByCmd map[*Command][]*AvailabilityRule
}

// validateAvailabilityRules checks one command's declared rules against the
// declared conditions. Called from validateCommand with the same path strings
// the rest of the surface validation uses.
func validateAvailabilityRules(path string, rules []*AvailabilityRule, declared map[string]bool) error {
	for i, rule := range rules {
		if err := validateAvailabilityRule(path, i, rule, declared); err != nil {
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
	case RuleEnabledWhen, RuleRequires, RuleDisables:
		if rule.When == nil {
			return UsageErrorf(
				"command %q availability rule %d (%s) requires a condition expression",
				path, index, rule.Type,
			)
		}
		if err := validateAvailabilityExpr(path, rule.When, declared); err != nil {
			return err
		}
	case RuleAnyOf:
		if len(rule.Rules) == 0 {
			return UsageErrorf("command %q availability rule %d (anyOf) has no rules", path, index)
		}
		// All operands must gate the same tool. The consumer's composite
		// constructors enforce this too, so validating here keeps the
		// failure at the declaration site.
		if err := validateSameField(path, index, "anyOf", rule.Rules); err != nil {
			return err
		}
		for i, inner := range rule.Rules {
			if err := validateAvailabilityRule(path, i, inner, declared); err != nil {
				return err
			}
		}
	case RuleEitherOf:
		if rule.Group == "" {
			return UsageErrorf(
				"command %q availability rule %d (eitherOf) requires a group name", path, index,
			)
		}
		if len(rule.Branches) == 0 {
			return UsageErrorf("command %q availability rule %d (eitherOf) has no branches", path, index)
		}
		var flat []*AvailabilityRule
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
			flat = append(flat, branch...)
		}
		if err := validateSameField(path, index, "eitherOf", flat); err != nil {
			return err
		}
		for name, branch := range rule.Branches {
			for i, inner := range branch {
				if err := validateAvailabilityRule(path, i, inner, declared); err != nil {
					return err
				}
			}
			_ = name
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
	case OpCond, OpCondEq, OpCondIn:
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
	case OpAnd, OpOr:
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
	case OpNot:
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

// validateSameField enforces the composite contract all operands gate the
// same tool: distinct explicit fields fail; empty fields inherit the
// declaring command's tool and pass. The Umpire runtime panics on this
// violation, so it is caught at the declaration site instead.
func validateSameField(path string, index int, kind string, rules []*AvailabilityRule) error {
	explicit := ""
	for _, inner := range rules {
		if inner == nil {
			continue
		}
		if inner.Field != "" {
			if explicit == "" {
				explicit = inner.Field
			} else if explicit != inner.Field {
				return UsageErrorf(
					"command %q availability rule %d (%s) operands target different tools %q and %q",
					path, index, kind, explicit, inner.Field,
				)
			}
		}
	}
	return nil
}
