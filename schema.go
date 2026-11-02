package clicontract

// The schema derivation: one rendering of the declared command surface for
// machine consumers. The app composes its full contract document around
// these pieces (statuses, error codes, and envelope fields are app facts).

// FlagSchema describes one flag as data: the value kind, the usage string,
// and the default (the flag's zero value unless the declaration sets one).
type FlagSchema struct {
	Kind    string `json:"kind"`
	Usage   string `json:"usage"`
	Default any    `json:"default"`
}

// CommandSchema describes one command as data: its help short, its Use
// string, whether it takes a URL positional, its flags, and (for nested
// commands) its subcommands.
type CommandSchema struct {
	Short       string                   `json:"short"`
	Use         string                   `json:"use"`
	URL         bool                     `json:"url_positional"`
	Flags       map[string]FlagSchema    `json:"flags,omitempty"`
	Subcommands map[string]CommandSchema `json:"subcommands,omitempty"`
}

// hasURLPositional reports whether the command takes exactly one required
// positional: the shape the envelope contract calls a URL target.
func hasURLPositional(c *Command) bool {
	return len(c.Positionals) == 1 && c.Positionals[0].Required
}

// KindSchema names the value kind the schema document renders.
func (k Kind) KindSchema() string {
	switch k {
	case Bool:
		return "bool"
	case Repeat:
		return "repeat"
	default:
		return "string"
	}
}

// defaultOf is the flag's default: the declared Default when set, the
// flag's zero value otherwise.
func defaultOf(f Flag) any {
	if f.Default != nil {
		return f.Default
	}
	switch f.Kind {
	case Bool:
		return false
	case Repeat:
		return []string{}
	default:
		return ""
	}
}

// flagSchema renders one flag as data.
func flagSchema(f Flag) FlagSchema {
	return FlagSchema{Kind: f.Kind.KindSchema(), Usage: f.Usage, Default: defaultOf(f)}
}

// flagsSchema maps one command's flags by name.
func flagsSchema(flags []Flag) map[string]FlagSchema {
	out := make(map[string]FlagSchema, len(flags))
	for _, f := range flags {
		out[f.Name] = flagSchema(f)
	}
	return out
}

// Schema renders one command's schema entry, nested subcommands included.
func (c *Command) Schema() CommandSchema {
	entry := CommandSchema{
		Short: c.Short,
		Use:   commandUse(c),
		URL:   hasURLPositional(c),
		Flags: flagsSchema(c.Flags),
	}
	if len(c.Subcommands) > 0 {
		entry.URL = false
		entry.Flags = nil
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
