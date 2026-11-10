package clicontract

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Umpire rule declarations. A command carries two independent rule sets over
// one vocabulary:
//
//   - Command.Availability gates whether the command's tool is offered,
//     evaluated against inputs the consumer supplies at load time (profile
//     mode, permission level, feature flags).
//   - Command.InputRules gates whether one invocation is legal, over the
//     command's own flags or, for a fieldless requires/enabledWhen rule, the
//     invocation as a whole.
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
// command's tool. In the input set a fieldless requires or enabledWhen rule
// applies to the invocation as a whole. Its condition must hold even when the
// caller supplies no flags.
type Rule struct {
	// Type is one of "enabledWhen", "requires", "disables", "anyOf",
	// "eitherOf".
	Type string `json:"type"`
	// Field is the rule's subject: the tool it gates in the availability
	// set, the flag it constrains in the input set. Empty means the declaring
	// command's tool in the availability set. On a requires or enabledWhen
	// rule in the input set, it means the invocation as a whole. A disables
	// rule acts through its Targets, and composite rules (anyOf, eitherOf)
	// carry no field.
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
// In the input set, Enabled with no field gates the invocation as a whole. An
// empty reason takes the default "condition not met".
func Enabled(when *Expr, reason string) *Rule {
	return enabledRule("", RuleEnabledWhen, when, reason, "condition not met")
}

// EnabledOn is Enabled targeting an explicitly named subject: a tool in the
// availability set, a flag in the input set. An enabledWhen rule adds no
// ordering edge, so this is the constructor for flags that must be supplied
// together: gate each flag on the other.
func EnabledOn(field string, when *Expr, reason string) *Rule {
	return enabledRule(field, RuleEnabledWhen, when, reason, "condition not met")
}

// Requires keeps the declaring command's tool gated until the condition is
// met. In the input set, Requires with no field declares a condition every
// invocation must satisfy. An empty reason takes the default "required
// condition not met".
func Requires(when *Expr, reason string) *Rule {
	return enabledRule("", RuleRequires, when, reason, "required condition not met")
}

// RequiresOn is Requires targeting an explicitly named subject: a tool in the
// availability set, a flag in the input set. A requires rule adds a dependency
// edge, so two rules pointing at each other are a cycle and fail validation;
// gate each flag on the other with EnabledOn instead.
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
// same subject: one named flag, or the invocation on a fieldless operand in
// the input set. In the availability set a fieldless operand takes the
// declaring command's tool.
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
		if err := ValidateLeafOnly(path, cmd); err != nil {
			return err
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
	durations := make(map[string]Kind, len(flags))
	for _, f := range flags {
		fields[f.Name] = true
		if f.Kind == Duration || f.Kind == DurationMS {
			durations[f.Name] = f.Kind
		}
	}
	set := ruleSet{label: "input", fields: fields, durations: durations}
	for i, rule := range rules {
		if err := validateRule(path, i, rule, set); err != nil {
			return err
		}
	}
	// A rule set that passes the checks above but that Umpire's schema rejects
	// would otherwise fail at first dispatch, where it reads as a caller error
	// instead of the declaration defect it is.
	if _, _, err := compileInputRules(flags, rules); err != nil {
		return inputRuleCompileError(path, err)
	}
	return nil
}

// cycleMarker is the phrase Umpire's schema compiler uses when the ordering
// edges derived from requires rules form a loop. It is a hint, not a contract:
// an unrecognized failure falls through to the generic wording.
const cycleMarker = "Cycle detected: "

// inputRuleCompileError reports a rule set the Umpire runtime rejects. A cycle
// is the one such failure a writer fixes by choosing another constructor, so it
// names the loop and that constructor.
func inputRuleCompileError(path string, err error) error {
	if _, loop, ok := strings.Cut(err.Error(), cycleMarker); ok {
		return UsageErrorf(
			"command %q input rules form a dependency cycle (%s); requires adds an ordering edge, so flags that must be supplied together gate each other with EnabledOn",
			path, strings.TrimSpace(loop),
		)
	}
	return UsageErrorf("command %q input rules do not compile: %v", path, err)
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
	// durations maps a duration flag's name to its kind, so a comparison
	// value on one can be checked against the form evaluation compares.
	// Nil in the availability set, which has no flag kinds.
	durations map[string]Kind
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
			// command's tool; in the input set it means the invocation as a
			// whole. Only a named field resolves against the flag surface.
			if rule.Field != "" && !set.fields[rule.Field] {
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
		if kind, ok := set.durations[expr.Field]; ok {
			switch expr.Op {
			case OpEq:
				if err := validateDurationValue(path, set, expr.Field, kind, expr.Value); err != nil {
					return err
				}
			case OpIn, OpNotIn:
				for _, value := range expr.Values {
					if err := validateDurationValue(path, set, expr.Field, kind, value); err != nil {
						return err
					}
				}
			}
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

// validateDurationValue checks one comparison value against a duration flag
// of the given kind. Evaluation compares a fixed form per kind, so a value
// outside that form is a declaration defect no invocation can satisfy.
func validateDurationValue(path string, set ruleSet, field string, kind Kind, value any) error {
	switch kind {
	case DurationMS:
		return durationMSValue(path, set, field, value)
	default: // Duration
		return canonicalDurationValue(path, set, field, value)
	}
}

// canonicalDurationValue checks one comparison value against a Go-duration
// flag. Evaluation compares the canonical Go duration string, so a value a
// caller can never satisfy — a non-string, an unparseable spelling, or a
// parseable but non-canonical one like 0m — is a declaration defect.
func canonicalDurationValue(path string, set ruleSet, field string, value any) error {
	s, ok := value.(string)
	if !ok {
		return UsageErrorf(
			"command %q %s expression compares duration flag %q against %v; a duration comparison value is a Go duration string",
			path, set.label, field, value,
		)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return UsageErrorf(
			"command %q %s expression compares duration flag %q against %q: %v",
			path, set.label, field, s, err,
		)
	}
	if d.String() != s {
		return UsageErrorf(
			"command %q %s expression compares duration flag %q against %q; write the canonical spelling %q, the form rule evaluation compares",
			path, set.label, field, s, d.String(),
		)
	}
	return nil
}

// durationMSValue checks one comparison value against a duration_ms flag:
// evaluation compares whole-number milliseconds, so a non-number or a
// fraction is a declaration defect no invocation can satisfy.
func durationMSValue(path string, set ruleSet, field string, value any) error {
	// A rule value past the milliseconds bound can never match: the
	// parser rejects the input that would bind there (tree.go's parse
	// path enforces the same bound).
	const maxMS = math.MaxInt64 / int64(time.Millisecond)
	outOfRange := "a duration_ms comparison value is a whole number of milliseconds within ±%d"
	switch n := value.(type) {
	case int:
		if int64(n) > maxMS || int64(n) < -maxMS {
			return UsageErrorf("command %q %s expression compares duration flag %q against %v; "+outOfRange, path, set.label, field, value, maxMS)
		}
		return nil
	case int64:
		if n > maxMS || n < -maxMS {
			return UsageErrorf("command %q %s expression compares duration flag %q against %v; "+outOfRange, path, set.label, field, value, maxMS)
		}
		return nil
	case float64:
		if n != math.Trunc(n) || n > float64(maxMS) || n < -float64(maxMS) {
			return UsageErrorf("command %q %s expression compares duration flag %q against %v; "+outOfRange, path, set.label, field, value, maxMS)
		}
		return nil
	default:
		return UsageErrorf("command %q %s expression compares duration flag %q against %v; a duration_ms comparison value is a whole number of milliseconds", path, set.label, field, value)
	}
}

// validateSameField enforces the composite contract all operands target the
// same subject. In the availability set an empty field inherits the declaring
// command's tool, so it passes against any reference. In the input set an empty
// field is the invocation itself, so distinct named flags cannot share it. The
// Umpire runtime panics on this violation, so it is caught at the declaration
// site instead.
func validateSameField(path string, index int, set ruleSet, kind string, rules []*Rule) error {
	reference := ""
	seen := false
	for _, inner := range rules {
		if inner == nil {
			continue
		}
		if inner.Field == "" && set.fields == nil {
			continue
		}
		if !seen {
			reference = inner.Field
			seen = true
			continue
		}
		if inner.Field == reference {
			continue
		}
		if reference != "" && inner.Field != "" {
			return UsageErrorf(
				"command %q %s rule %d (%s) operands target different %s %q and %q",
				path, set.label, index, kind, set.subject(), reference, inner.Field,
			)
		}
		return UsageErrorf(
			"command %q %s rule %d (%s) operands target different subjects: %s and %s",
			path, set.label, index, kind, set.subjectName(reference), set.subjectName(inner.Field),
		)
	}
	return nil
}

// subjectName renders one rule subject for diagnostics.
func (s ruleSet) subjectName(field string) string {
	if field == "" {
		return "the invocation"
	}
	return fmt.Sprintf("%s %q", s.subject(), field)
}

// subject names what a set's fields refer to, for diagnostics.
func (s ruleSet) subject() string {
	if s.fields != nil {
		return "flags"
	}
	return "tools"
}
