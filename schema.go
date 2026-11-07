package clicontract

import "time"

// The schema derivation: one rendering of the declared command surface for
// machine consumers. SchemaDocument wraps these entries into the document the
// cli-contract spec describes; statuses, error codes, and envelope fields stay
// app facts.

// FlagSchema describes one flag as data: the value kind, the declared
// shorthand, the usage string, the default (the flag's zero value unless the
// declaration sets one), and whether the flag is required.
type FlagSchema struct {
	Kind     string `json:"kind"`
	Short    string `json:"short,omitempty"`
	Usage    string `json:"usage"`
	Default  any    `json:"default"`
	Required bool   `json:"required,omitempty"`
}

// CommandSchema describes one command as data: its help short and long, its
// Use string, its positionals, whether it takes a URL positional, its flags,
// its input and availability rules, its output declaration, and (for nested
// commands) its subcommands.
//
// Use renders the same positionals in help form; Positionals is the
// authoritative list and a reader that rebuilds a declaration reads it, not
// Use. A command that owns subcommands keeps its persistent flags under Flags
// and declares no positionals of its own.
type CommandSchema struct {
	Short          string                   `json:"short"`
	Long           string                   `json:"long,omitempty"`
	Use            string                   `json:"use"`
	Positionals    []Positional             `json:"positionals,omitempty"`
	URL            bool                     `json:"url_positional"`
	NoInterspersed bool                     `json:"no_interspersed,omitempty"`
	Flags          map[string]FlagSchema    `json:"flags,omitempty"`
	InputRules     []Rule                   `json:"input_rules,omitempty"`
	Availability   []Rule                   `json:"availability,omitempty"`
	Output         *Output                  `json:"output,omitempty"`
	Subcommands    map[string]CommandSchema `json:"subcommands,omitempty"`
}

// urlPositional reports whether a positional list is the shape the envelope
// contract calls a URL target: exactly one required positional.
func urlPositional(positionals []Positional) bool {
	return len(positionals) == 1 && positionals[0].Required
}

// hasURLPositional reports whether the command takes exactly one required
// positional: the shape the envelope contract calls a URL target.
func hasURLPositional(c *Command) bool {
	return urlPositional(c.Positionals)
}

// KindSchema names the value kind the schema document renders.
func (k Kind) KindSchema() string {
	for _, entry := range kindNames {
		if entry.kind == k {
			return entry.name
		}
	}
	return "string"
}

// defaultOf is the flag's default: the declared Default when set, the
// flag's zero value otherwise. A duration renders as its Go duration string so
// a language-neutral document never carries Go's nanosecond count.
func defaultOf(f Flag) any {
	if f.Default != nil {
		if d, ok := f.Default.(time.Duration); ok {
			return d.String()
		}
		return f.Default
	}
	switch f.Kind {
	case Bool:
		return false
	case Repeat:
		return []string{}
	case Int:
		return 0
	case Duration:
		return time.Duration(0).String()
	default:
		return ""
	}
}

// flagSchema renders one flag as data.
func flagSchema(f Flag) FlagSchema {
	return FlagSchema{Kind: f.Kind.KindSchema(), Short: f.Short, Usage: f.Usage, Default: defaultOf(f), Required: f.Required}
}

// flagsSchema maps one command's flags by name.
func flagsSchema(flags []Flag) map[string]FlagSchema {
	out := make(map[string]FlagSchema, len(flags))
	for _, f := range flags {
		out[f.Name] = flagSchema(f)
	}
	return out
}

// ruleValues renders a declared rule list as data. Values rather than pointers
// keep the rendered document detached from the declaration the way FlagSchema
// does.
func ruleValues(rules []*Rule) []Rule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue // rejected at startup; skip rather than panic here
		}
		out = append(out, *rule)
	}
	return out
}

// outputValue renders one command's output declaration, copying it so the
// document stays detached from the declaration. The zero value renders as
// absent rather than an empty mode.
func outputValue(o Output) *Output {
	if o.Mode == "" {
		return nil
	}
	out := o
	out.SuccessExitCodes = append([]int(nil), o.SuccessExitCodes...)
	return &out
}

// Schema renders one command's schema entry, nested subcommands included.
func (c *Command) Schema() CommandSchema {
	entry := CommandSchema{
		Short:          c.Short,
		Long:           c.Long,
		Use:            commandUse(c),
		Positionals:    append([]Positional(nil), c.Positionals...),
		URL:            hasURLPositional(c),
		NoInterspersed: c.NoInterspersed,
		Flags:          flagsSchema(c.Flags),
		InputRules:     ruleValues(c.InputRules),
		Availability:   ruleValues(c.Availability),
		Output:         outputValue(c.Output),
	}
	if len(c.Subcommands) > 0 {
		// A parent parses only its persistent flags and runs no leaf
		// contract of its own. validate and the document walks reject a
		// parent that declares the leaf-only members; the zeroing here keeps
		// a direct Schemas() call — which validates nothing — from rendering
		// leaf-only state into the wire.
		entry.URL = false
		entry.Positionals = nil
		entry.NoInterspersed = false
		entry.Output = nil
		entry.InputRules = nil
		entry.Availability = nil
		entry.Flags = nil
		if len(c.PersistentFlags) > 0 {
			// Subcommands inherit these; the parent's entry is where
			// consumers look for them.
			entry.Flags = flagsSchema(c.PersistentFlags)
		}
		entry.Subcommands = make(map[string]CommandSchema, len(c.Subcommands))
		for _, sub := range c.Subcommands {
			entry.Subcommands[sub.Name] = sub.Schema()
		}
	}
	return entry
}

// Schemas renders every declared command's schema entry, keyed by command
// name (nested subcommands live under their parent's entry).
func (a *App) Schemas() map[string]CommandSchema {
	out := make(map[string]CommandSchema, len(a.Commands))
	for _, c := range a.Commands {
		out[c.Name] = c.Schema()
	}
	return out
}

// CommonFlagSchemas renders the common flags section.
func (a *App) CommonFlagSchemas() map[string]FlagSchema {
	return flagsSchema(a.CommonFlags)
}
