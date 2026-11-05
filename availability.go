package clicontract

import "strings"

// Umpire rule declarations. A command carries two independent rule sets over
// one vocabulary:
//
//   - Command.Availability gates whether the command's tool is offered,
//     evaluated against inputs the consumer supplies at load time (profile
//     mode, permission level, feature flags).
//   - Command.InputRules gates whether one invocation is legal, over the
//     command's own flags.
//
// The sets differ in subject namespace and dialect, not in rule type: a rule's
// fields name tool names in the first set and flag names in the second. See the
// registry package and the cli-registry manifest spec for the consumer side.

// Availability condition types, matching the Umpire v1 condition types.
const (
	ConditionBoolean = "boolean"
	ConditionString  = "string"
	ConditionNumber  = "number"
	ConditionStrings = "string[]"
	ConditionNumbers = "number[]"
)

// Rule types, matching the Umpire v1 gate rules.
const (
	RuleEnabledWhen = "enabledWhen"
	RuleRequires    = "requires"
	RuleDisables    = "disables"
	RuleAnyOf       = "anyOf"
	RuleEitherOf    = "eitherOf"
)

// Condition expression ops, comparing inputs the consumer supplies at load.
const (
	OpCond   = "cond"
	OpCondEq = "condEq"
	OpCondIn = "condIn"
	OpAnd    = "and"
	OpOr     = "or"
	OpNot    = "not"
)

// Field expression ops, comparing the invocation's own inputs.
const (
	OpPresent = "present"
	OpAbsent  = "absent"
	OpTruthy  = "truthy"
	OpFalsy   = "falsy"
	OpEq      = "eq"
	OpIn      = "in"
	OpNotIn   = "notIn"
)

// Condition declares one availability input the consumer supplies at load.
type Condition struct {
	Name string
	// Type is one of the Condition* constants.
	Type string
	// Description documents the input for consumers.
	Description string
}

// Expr is one expression inside a rule. It has two dialects, matching Umpire's
// json-expr: the condition dialect (cond, condEq, condIn) compares values the
// consumer supplies at load, and the field dialect (present, absent, truthy,
// falsy, eq, in, notIn) compares the command's own inputs. A field expression
// names its subject in Field, a condition expression in Condition; the two are
// exclusive per expression tree, and the enclosing rule's slot fixes which
// dialect is valid. Build expressions with When, CondEq, CondIn, Present,
// Absent, Truthy, Falsy, Eq, In, NotIn, And, Or, and Not; the zero value is not
// valid.
type Expr struct {
	// Op is one of "cond", "condEq", "condIn", "and", "or", "not" (condition
	// dialect) or "present", "absent", "truthy", "falsy", "eq", "in",
	// "notIn" (field dialect).
	Op string `json:"op"`
	// Field is the compared property name (field dialect).
	Field string `json:"field,omitempty"`
	// Condition names a declared Condition (cond, condEq, condIn).
	Condition string `json:"condition,omitempty"`
	// Value is the comparison value (condEq, eq).
	Value any `json:"value,omitempty"`
	// Values are the membership values (condIn, in, notIn).
	Values []any `json:"values,omitempty"`
	// Exprs are the operands (and, or).
	Exprs []*Expr `json:"exprs,omitempty"`
	// Expr is the operand (not).
	Expr *Expr `json:"expr,omitempty"`
}

// Rule is one Umpire rule. Build rules with Enabled, EnabledOn, Requires,
// RequiresOn, Disables, AnyOf, and EitherOf, and expressions with the
// constructors above. A rule's Field and Targets name subjects in the
// namespace of the set it is declared in: tool names under
// Command.Availability, flag names under Command.InputRules. In the
// availability set a rule without an explicit Field applies to its own
// command's tool; an input rule must name the flag it constrains.
type Rule struct {
	// Type is one of "enabledWhen", "requires", "disables", "anyOf",
	// "eitherOf".
	Type string `json:"type"`
	// Field is the rule's subject: the tool it gates in the availability
	// set, the property it governs in the input set. Empty means the
	// declaring command's tool. Composite rules (anyOf, eitherOf) carry no
	// field.
	Field string `json:"field,omitempty"`
	// Targets are the subjects a disables rule turns off.
	Targets []string `json:"targets,omitempty"`
	// Group names an eitherOf rule; required for eitherOf.
	Group string `json:"group,omitempty"`
	// When is the rule's condition expression (every type but anyOf).
	When *Expr `json:"when,omitempty"`
	// Reason is reported when the rule fails. Each type carries a default.
	Reason string `json:"reason,omitempty"`
	// Rules are the operands (anyOf).
	Rules []*Rule `json:"rules,omitempty"`
	// Branches are the operands (eitherOf).
	Branches map[string][]*Rule `json:"branches,omitempty"`
}

// When references a declared boolean condition.
func When(condition string) *Expr {
	return &Expr{Op: OpCond, Condition: condition}
}

// CondEq compares a condition against a value.
func CondEq(condition string, value any) *Expr {
	return &Expr{Op: OpCondEq, Condition: condition, Value: value}
}

// CondIn tests condition membership.
func CondIn(condition string, values ...any) *Expr {
	return &Expr{Op: OpCondIn, Condition: condition, Values: values}
}

// Present passes when the invocation supplies the named property.
func Present(field string) *Expr {
	return &Expr{Op: OpPresent, Field: field}
}

// Absent passes when the invocation supplies no value for the named property.
func Absent(field string) *Expr {
	return &Expr{Op: OpAbsent, Field: field}
}

// Truthy passes when the named property holds a truthy value.
func Truthy(field string) *Expr {
	return &Expr{Op: OpTruthy, Field: field}
}

// Falsy passes when the named property holds a falsy value.
func Falsy(field string) *Expr {
	return &Expr{Op: OpFalsy, Field: field}
}

// Eq compares the named property's value against value.
func Eq(field string, value any) *Expr {
	return &Expr{Op: OpEq, Field: field, Value: value}
}

// In tests the named property's value for membership.
func In(field string, values ...any) *Expr {
	return &Expr{Op: OpIn, Field: field, Values: values}
}

// NotIn tests the named property's value for exclusion.
func NotIn(field string, values ...any) *Expr {
	return &Expr{Op: OpNotIn, Field: field, Values: values}
}

// And requires every operand to pass.
func And(exprs ...*Expr) *Expr {
	return &Expr{Op: OpAnd, Exprs: exprs}
}

// Or passes when any operand passes.
func Or(exprs ...*Expr) *Expr {
	return &Expr{Op: OpOr, Exprs: exprs}
}

// Not negates its operand.
func Not(expr *Expr) *Expr {
	return &Expr{Op: OpNot, Expr: expr}
}

// Enabled enables the declaring command's tool only when the expression passes.
// An empty reason takes the default "condition not met".
func Enabled(when *Expr, reason string) *Rule {
	return enabledRule("", RuleEnabledWhen, when, reason, "condition not met")
}

// EnabledOn is Enabled targeting an explicitly named subject: a tool in the
// availability set, a flag in the input set.
func EnabledOn(field string, when *Expr, reason string) *Rule {
	return enabledRule(field, RuleEnabledWhen, when, reason, "condition not met")
}

// Requires keeps the declaring command's tool gated until the condition is
// met. An empty reason takes the default "required condition not met".
func Requires(when *Expr, reason string) *Rule {
	return enabledRule("", RuleRequires, when, reason, "required condition not met")
}

// RequiresOn is Requires targeting an explicitly named subject: a tool in the
// availability set, a flag in the input set.
func RequiresOn(field string, when *Expr, reason string) *Rule {
	return enabledRule(field, RuleRequires, when, reason, "required condition not met")
}

func enabledRule(field, ruleType string, when *Expr, reason, defaultReason string) *Rule {
	if reason == "" {
		reason = defaultReason
	}
	return &Rule{Type: ruleType, Field: field, When: when, Reason: reason}
}

// Disables turns the named subjects off while the condition holds. An empty
// reason takes the default "overridden by condition".
func Disables(targets []string, when *Expr, reason string) *Rule {
	if reason == "" {
		reason = "overridden by condition"
	}
	return &Rule{Type: RuleDisables, Targets: targets, When: when, Reason: reason}
}

// AnyOf passes when any operand rule passes; all operands must target the
// same subject (an empty field on every operand takes the declaring command's
// tool, as in the availability set).
func AnyOf(rules ...*Rule) *Rule {
	return &Rule{Type: RuleAnyOf, Rules: rules}
}

// EitherOf passes when any branch's rules all pass. The group names the
// choice in the rendered manifest and in consumer diagnostics.
func EitherOf(group string, branches map[string][]*Rule) *Rule {
	return &Rule{Type: RuleEitherOf, Group: group, Branches: branches}
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

	checks := &availabilityChecks{
		declared:   declared,
		rulesByCmd: map[*Command][]*Rule{},
		inputByCmd: map[*Command][]*Rule{},
		toolNames:  map[string]bool{},
	}
	var collect func(path string, cmd *Command) error
	collect = func(path string, cmd *Command) error {
		if len(cmd.Subcommands) > 0 && (len(cmd.Availability) > 0 || len(cmd.InputRules) > 0) {
			return UsageErrorf(
				"command %q declares rules but owns subcommands; only leaf commands run as tools",
				path,
			)
		}
		if len(cmd.Subcommands) == 0 {
			checks.toolNames[ToolName(app.Name, path)] = true
		}
		if len(cmd.Availability) > 0 {
			checks.rulesByCmd[cmd] = cmd.Availability
		}
		if len(cmd.InputRules) > 0 {
			checks.inputByCmd[cmd] = cmd.InputRules
		}
		for _, sub := range cmd.Subcommands {
			if err := collect(path+" "+sub.Name, sub); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cmd := range app.Commands {
		if err := collect(cmd.Name, cmd); err != nil {
			return nil, err
		}
	}
	return checks, nil
}

// ToolName renders the registry tool name for a command path: the app name
// and the dash-joined command path ("mycli store reset" → "mycli-store-reset").
func ToolName(name, path string) string {
	return strings.ReplaceAll(name+" "+path, " ", "-")
}

// availabilityChecks carries the validated rule declarations into the shared
// command-tree walk: the declared condition names, the rules declared per
// command (leaf commands only, one map per declaration slot), the generated
// tool names, and the availability field references that resolve against that
// set after the walk completes.
type availabilityChecks struct {
	declared   map[string]bool
	rulesByCmd map[*Command][]*Rule
	inputByCmd map[*Command][]*Rule
	toolNames  map[string]bool
	fieldRefs  []fieldRef
}

// fieldRef is one explicit Field on a leaf rule, recorded during the walk
// and resolved after the whole tree is known.
type fieldRef struct {
	path  string
	index int
	field string
}

// validateAvailabilityRules checks one command's declared availability rules
// against the declared conditions and records explicit field references for
// resolution after the walk. Called from validateCommand with the same path
// strings the rest of the surface validation uses.
func validateAvailabilityRules(path string, rules []*Rule, checks *availabilityChecks) error {
	set := ruleSet{label: "availability", declared: checks.declared}
	for i, rule := range rules {
		if err := validateRule(path, i, rule, set); err != nil {
			return err
		}
		if rule.Field != "" && (rule.Type == RuleEnabledWhen || rule.Type == RuleRequires) {
			checks.fieldRefs = append(checks.fieldRefs, fieldRef{path: path, index: i, field: rule.Field})
		}
	}
	return nil
}

// exposableSurface lists the flags a tool can carry properties for: the app's
// common flags and the leaf's own. An ancestor's persistent flag parses on the
// leaf but cannot appear in the tool's argv binding, because the binding has no
// way to place it before the subcommand name. A rule naming such a flag is
// therefore not expressible in a manifest, and is rejected at the declaration
// site instead.
func exposableSurface(common, own []Flag) []Flag {
	out := make([]Flag, 0, len(common)+len(own))
	out = append(out, common...)
	return append(out, own...)
}

// validateInputRules checks one command's input rules against the flags the
// tool can expose, so a rule naming a flag no property can carry fails at the
// declaration site rather than in a generated manifest.
func validateInputRules(path string, rules []*Rule, flags []Flag) error {
	fields := make(map[string]bool, len(flags))
	for _, f := range flags {
		fields[f.Name] = true
	}
	set := ruleSet{label: "input", fields: fields}
	for i, rule := range rules {
		if err := validateRule(path, i, rule, set); err != nil {
			return err
		}
	}
	// A rule set that passes the checks above but that Umpire's schema rejects
	// would otherwise fail at first dispatch, where it reads as a caller error
	// instead of the declaration defect it is.
	if _, err := compileInputRules(flags, rules); err != nil {
		return UsageErrorf("command %q input rules do not compile: %v", path, err)
	}
	return nil
}

// ruleSet identifies which declaration slot a rule list came from. The two
// sets share rule types and validation; they differ in expression dialect and
// in the namespace a rule's Field and Targets name.
type ruleSet struct {
	// label names the set in caller errors.
	label string
	// declared is the condition namespace: the App's declared conditions in
	// the availability set.
	declared map[string]bool
	// fields is the subject namespace for field expressions and disables
	// targets: a command's flag names in the input set. Nil in the
	// availability set, whose explicit fields resolve against the tool set
	// after the walk.
	fields map[string]bool
}

// validateRule checks one rule against the set it was declared in, recursing
// through composites.
func validateRule(path string, index int, rule *Rule, set ruleSet) error {
	if rule == nil {
		return UsageErrorf("command %q %s rule %d is nil", path, set.label, index)
	}
	switch rule.Type {
	case RuleEnabledWhen, RuleRequires, RuleDisables:
		if rule.Type == RuleDisables {
			if len(rule.Targets) == 0 {
				return UsageErrorf(
					"command %q %s rule %d (disables) has no targets; the rule would be inert",
					path, set.label, index,
				)
			}
			for _, target := range rule.Targets {
				if set.fields != nil && !set.fields[target] {
					return UsageErrorf(
						"command %q %s rule %d (disables) targets unknown flag %q",
						path, set.label, index, target,
					)
				}
			}
		}
		if rule.Type != RuleDisables && set.fields != nil {
			// In the availability set an empty Field means the declaring
			// command's tool; an input rule has no such default, so the flag
			// it constrains is mandatory and must exist.
			if rule.Field == "" {
				return UsageErrorf(
					"command %q %s rule %d (%s) has no field; input rules name the flag they constrain",
					path, set.label, index, rule.Type,
				)
			}
			if !set.fields[rule.Field] {
				return UsageErrorf(
					"command %q %s rule %d (%s) names unknown flag %q",
					path, set.label, index, rule.Type, rule.Field,
				)
			}
		}
		if rule.When == nil {
			return UsageErrorf(
				"command %q %s rule %d (%s) requires a condition expression",
				path, set.label, index, rule.Type,
			)
		}
		if err := validateExpr(path, rule.When, set); err != nil {
			return err
		}
	case RuleAnyOf:
		if len(rule.Rules) == 0 {
			return UsageErrorf("command %q %s rule %d (anyOf) has no rules", path, set.label, index)
		}
		// All operands must gate the same tool. The consumer's composite
		// constructors enforce this too, so validating here keeps the
		// failure at the declaration site.
		if err := validateSameField(path, index, set, "anyOf", rule.Rules); err != nil {
			return err
		}
		for i, inner := range rule.Rules {
			if err := validateRule(path, i, inner, set); err != nil {
				return err
			}
		}
	case RuleEitherOf:
		if rule.Group == "" {
			return UsageErrorf(
				"command %q %s rule %d (eitherOf) requires a group name", path, set.label, index,
			)
		}
		if len(rule.Branches) == 0 {
			return UsageErrorf("command %q %s rule %d (eitherOf) has no branches", path, set.label, index)
		}
		var flat []*Rule
		for name, branch := range rule.Branches {
			if name == "" {
				return UsageErrorf(
					"command %q %s rule %d (eitherOf) has an unnamed branch", path, set.label, index,
				)
			}
			if len(branch) == 0 {
				return UsageErrorf(
					"command %q %s rule %d (eitherOf) branch %q is empty", path, set.label, index, name,
				)
			}
			flat = append(flat, branch...)
		}
		if err := validateSameField(path, index, set, "eitherOf", flat); err != nil {
			return err
		}
		for _, branch := range rule.Branches {
			for i, inner := range branch {
				if err := validateRule(path, i, inner, set); err != nil {
					return err
				}
			}
		}
	default:
		return UsageErrorf(
			"command %q %s rule %d has unknown type %q", path, set.label, index, rule.Type,
		)
	}
	return nil
}

// validateExpr checks one expression against the dialect its set allows. The
// two dialects are exclusive: a condition op in the input set, or a field op
// in the availability set, is a caller error rather than a mixed expression
// the Umpire contract would reject on the wire.
func validateExpr(path string, expr *Expr, set ruleSet) error {
	if expr == nil {
		return UsageErrorf("command %q %s expression is nil", path, set.label)
	}
	switch expr.Op {
	case OpCond, OpCondEq, OpCondIn:
		if set.fields != nil {
			return UsageErrorf(
				"command %q %s %q expression compares consumer conditions; the input set compares the command's own inputs",
				path, set.label, expr.Op,
			)
		}
		if expr.Field != "" {
			return UsageErrorf(
				"command %q %s %q expression also names a field; the dialects do not mix",
				path, set.label, expr.Op,
			)
		}
		if expr.Condition == "" {
			return UsageErrorf(
				"command %q %s %q expression has no condition", path, set.label, expr.Op,
			)
		}
		if !set.declared[expr.Condition] {
			return UsageErrorf(
				"command %q %s expression references undeclared condition %q",
				path, set.label, expr.Condition,
			)
		}
	case OpPresent, OpAbsent, OpTruthy, OpFalsy, OpEq, OpIn, OpNotIn:
		if set.fields == nil {
			return UsageErrorf(
				"command %q %s %q expression compares the command's own inputs; the availability set compares consumer conditions",
				path, set.label, expr.Op,
			)
		}
		if expr.Condition != "" {
			return UsageErrorf(
				"command %q %s %q expression also names a condition; the dialects do not mix",
				path, set.label, expr.Op,
			)
		}
		if expr.Field == "" {
			return UsageErrorf(
				"command %q %s %q expression has no field", path, set.label, expr.Op,
			)
		}
		if !set.fields[expr.Field] {
			return UsageErrorf(
				"command %q %s expression references unknown flag %q", path, set.label, expr.Field,
			)
		}
	case OpAnd, OpOr:
		if len(expr.Exprs) == 0 {
			return UsageErrorf(
				"command %q %s %q expression has no operands", path, set.label, expr.Op,
			)
		}
		for _, inner := range expr.Exprs {
			if err := validateExpr(path, inner, set); err != nil {
				return err
			}
		}
	case OpNot:
		if expr.Expr == nil {
			return UsageErrorf("command %q %s not expression has no operand", path, set.label)
		}
		return validateExpr(path, expr.Expr, set)
	default:
		return UsageErrorf(
			"command %q %s expression has unknown op %q", path, set.label, expr.Op,
		)
	}
	return nil
}

// validateSameField enforces the composite contract all operands target the
// same subject: distinct explicit fields fail; empty fields inherit the
// declaring command's tool and pass. The Umpire runtime panics on this
// violation, so it is caught at the declaration site instead.
func validateSameField(path string, index int, set ruleSet, kind string, rules []*Rule) error {
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
					"command %q %s rule %d (%s) operands target different %s %q and %q",
					path, set.label, index, kind, set.subject(), explicit, inner.Field,
				)
			}
		}
	}
	return nil
}

// subject names what a set's fields refer to, for diagnostics.
func (s ruleSet) subject() string {
	if s.fields != nil {
		return "flags"
	}
	return "tools"
}
