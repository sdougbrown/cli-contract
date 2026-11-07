package clicontract

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// The schema document: the declared command surface and the behavior the
// library fixes, rendered as one machine-readable value. Apps encode it and
// add their own facts (statuses, error codes, envelope fields, exit codes)
// around it; the cli-contract specification describes the whole document.

// SchemaDocumentV1 identifies the document shape this library renders. The
// spec calls this shape v1; a document that changes incompatibly gets a new
// id.
const SchemaDocumentV1 = "cli-contract/cli-schema/v1"

// ConditionSchema describes one availability input. The declaration's Name is
// the map key, so it is not repeated here. The registry manifest mirrors this
// block with its own local shape (registry/conditionShape) — the manifest wire
// must not move when this struct does, so edits land in both declarations.
type ConditionSchema struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// SchemaDocument is one app's declared surface as data: the commands, the
// common flags, the availability conditions, and the signal convention the
// library owns. Empty optional sections are omitted rather than rendered as
// zero values.
type SchemaDocument struct {
	Schema              string                     `json:"schema"`
	Name                string                     `json:"name,omitempty"`
	Version             string                     `json:"version,omitempty"`
	Commands            map[string]CommandSchema   `json:"commands"`
	CommonFlags         map[string]FlagSchema      `json:"common_flags,omitempty"`
	Conditions          map[string]ConditionSchema `json:"conditions,omitempty"`
	Signals             map[string]int             `json:"signals,omitempty"`
	HandlerOwnedSignals bool                       `json:"handler_owned_signals,omitempty"`
	// ExitCodes is the app's vocabulary of named exit codes. The library
	// leaves it empty: only the adapter knows the app's statuses.
	ExitCodes map[string]int `json:"exit_codes,omitempty"`
}

// ParseSchemaDocument decodes one schema document. The parser is lenient about
// unknown members — it decodes what it knows and drops the rest — while the
// spec's JSON Schema is the strict validator: it rejects unknown members
// outright, and the only app facts it accepts are the reserved docs,
// typical_flow, cli_version, and envelope. An author who adds a custom fact
// must name it in the spec, or the strict validator will fail the document.
func ParseSchemaDocument(data []byte) (*SchemaDocument, error) {
	var doc SchemaDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("schema document: %w", err)
	}
	if doc.Schema == "" {
		return nil, fmt.Errorf("schema document: no schema id")
	}
	if doc.Schema != SchemaDocumentV1 {
		return nil, fmt.Errorf("schema document: unsupported schema id %q, want %q", doc.Schema, SchemaDocumentV1)
	}
	return &doc, nil
}

// App builds the declaration one document describes. handlers maps a leaf
// command's full path ("store reset") to its handler; every leaf needs one,
// and a parent needs none. The document does not carry the declaration-order
// facts a Go declaration has (root help text, bare-help behavior, error
// prefix), so the built app uses the library defaults for those.
//
// A document is either a rendering of a declaration that already ran or a
// hand-written one, so App rejects what the JSON Schema cannot express: a
// default that does not match its flag's kind, an unknown kind, and a parent
// that declares leaf-only members. The rebuilt app shares its rules' expression
// state and its declared output slice with the document it came from — the
// render direction copies, the apply direction aliases; mutate the document
// and the app moves with it. The v1 document carries no handler-class marker,
// so every rebuilt leaf runs on the Handle dispatch path: a document rendered
// from Raw or Stream leaves rebuilds with the library's adapter writing its
// output, not the writer those handlers own.
//
// Commands and flags are ordered by name: a JSON object has no order, and the
// tree sorts commands anyway.
func (d *SchemaDocument) App(handlers map[string]HandleFunc) (*App, error) {
	if len(d.Commands) == 0 {
		return nil, fmt.Errorf("schema document %q declares no commands", d.Schema)
	}
	common, err := flagsFromSchema("common flag", d.CommonFlags)
	if err != nil {
		return nil, fmt.Errorf("schema document %q: %w", d.Schema, err)
	}
	app := &App{
		Name:                   d.Name,
		Version:                d.Version,
		HandlerOwnedSignals:    d.HandlerOwnedSignals,
		CommonFlags:            common,
		AvailabilityConditions: conditionsFromSchema(d.Conditions),
	}
	for _, name := range slices.Sorted(maps.Keys(d.Commands)) {
		cmd, err := commandFromSchema(name, d.Commands[name], handlers)
		if err != nil {
			return nil, fmt.Errorf("schema document %q: %w", d.Schema, err)
		}
		app.Commands = append(app.Commands, cmd)
	}
	// The rebuilt declaration must satisfy the same rules a Go declaration
	// does: help text, shorthand collisions, leaf-only shapes. Failing here
	// names the defect; failing at first Tree buries it in a run.
	if err := validate(app); err != nil {
		return nil, fmt.Errorf("schema document %q: %w", d.Schema, err)
	}
	return app, nil
}

// commandFromSchema builds one command and its subcommands. path is the full
// command path, which is also the handler key.
func commandFromSchema(path string, s CommandSchema, handlers map[string]HandleFunc) (*Command, error) {
	cmd := &Command{
		Name:           path[strings.LastIndex(path, " ")+1:],
		Short:          s.Short,
		Long:           s.Long,
		Positionals:    append([]Positional(nil), s.Positionals...),
		NoInterspersed: s.NoInterspersed,
		InputRules:     rulesFromSchema(s.InputRules),
		Availability:   rulesFromSchema(s.Availability),
	}
	if s.Output != nil {
		cmd.Output = *s.Output
	}
	if want := urlPositional(cmd.Positionals); s.URL != want {
		return nil, fmt.Errorf(
			"command %q declares url_positional %t but its positionals say %t",
			path, s.URL, want,
		)
	}
	if len(s.Subcommands) > 0 {
		// A parent parses only its persistent flags; the tree attaches no
		// positional or output handling to it, so accepting these would
		// build a command the document does not describe.
		flags, err := flagsFromSchema("command "+path, s.Flags)
		if err != nil {
			return nil, err
		}
		cmd.PersistentFlags = flags
		for _, name := range slices.Sorted(maps.Keys(s.Subcommands)) {
			sub, err := commandFromSchema(path+" "+name, s.Subcommands[name], handlers)
			if err != nil {
				return nil, err
			}
			cmd.Subcommands = append(cmd.Subcommands, sub)
		}
		// Leaf-only and output-shape checks run once, in App's validate —
		// not per command here.
		return cmd, nil
	}
	flags, err := flagsFromSchema("command "+path, s.Flags)
	if err != nil {
		return nil, err
	}
	cmd.Flags = flags
	handle, ok := handlers[path]
	if !ok {
		return nil, fmt.Errorf("command %q declares no handler", path)
	}
	cmd.Handle = handle
	if err := cmd.ValidateOutput(path); err != nil {
		return nil, err
	}
	return cmd, nil
}

// flagsFromSchema rebuilds a flag list. The document keys flags by name, so
// the list comes back ordered by name; where names the declaration the flags
// belong to, for the error message.
func flagsFromSchema(where string, flags map[string]FlagSchema) ([]Flag, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	out := make([]Flag, 0, len(flags))
	for _, name := range slices.Sorted(maps.Keys(flags)) {
		f, err := flagFromSchema(name, flags[name])
		if err != nil {
			return nil, fmt.Errorf("%s flag %q: %w", where, name, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// flagFromSchema rebuilds one flag, converting its rendered default back to
// the declaration's type. A default the kind cannot apply is an error: the
// parser would otherwise silently apply the zero value.
func flagFromSchema(name string, s FlagSchema) (Flag, error) {
	kind, err := kindFromSchema(s.Kind)
	if err != nil {
		return Flag{}, err
	}
	f := Flag{Name: name, Kind: kind, Short: s.Short, Usage: s.Usage, Required: s.Required}
	if s.Default == nil {
		return f, nil
	}
	switch kind {
	case Int:
		switch number := s.Default.(type) {
		case int:
			f.Default = number
		case float64:
			if number != math.Trunc(number) {
				return Flag{}, fmt.Errorf("integer default %v is not a whole number", number)
			}
			if number > math.MaxInt64 || number < math.MinInt64 {
				return Flag{}, fmt.Errorf("integer default %v is out of int range", number)
			}
			f.Default = int(number)
		default:
			return Flag{}, fmt.Errorf("integer default %v is not a number", s.Default)
		}
	case Duration:
		switch value := s.Default.(type) {
		case time.Duration:
			f.Default = value
		case string:
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return Flag{}, fmt.Errorf("duration default %q: %w", value, err)
			}
			f.Default = parsed
		default:
			return Flag{}, fmt.Errorf("duration default %v is not a duration string", s.Default)
		}
	case Bool:
		value, ok := s.Default.(bool)
		if !ok {
			return Flag{}, fmt.Errorf("bool default %v is not a boolean", s.Default)
		}
		f.Default = value
	case Repeat:
		switch values := s.Default.(type) {
		case []string:
			f.Default = append([]string(nil), values...)
		case []any:
			repeated := make([]string, 0, len(values))
			for _, value := range values {
				text, ok := value.(string)
				if !ok {
					return Flag{}, fmt.Errorf("repeat default %v is not an array of strings", s.Default)
				}
				repeated = append(repeated, text)
			}
			f.Default = repeated
		default:
			return Flag{}, fmt.Errorf("repeat default %v is not an array", s.Default)
		}
	default:
		value, ok := s.Default.(string)
		if !ok {
			return Flag{}, fmt.Errorf("string default %v is not a string", s.Default)
		}
		f.Default = value
	}
	return f, nil
}

// kindNames is the flag-kind vocabulary in one place: each Kind and the wire
// name the schema document carries for it. KindSchema and kindFromSchema both
// derive from it, so a kind added here renders and rebuilds together.
var kindNames = []struct {
	kind Kind
	name string
}{
	{String, "string"},
	{Bool, "bool"},
	{Repeat, "repeat"},
	{Int, "integer"},
	{Duration, "duration"},
}

// kindFromSchema maps a rendered kind name back to the declaration's kind.
func kindFromSchema(kind string) (Kind, error) {
	for _, entry := range kindNames {
		if entry.name == kind {
			return entry.kind, nil
		}
	}
	return String, fmt.Errorf("unknown kind %q", kind)
}

// rulesFromSchema rebuilds a declared rule list as pointers, the shape the
// declaration uses.
func rulesFromSchema(rules []Rule) []*Rule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]*Rule, 0, len(rules))
	for i := range rules {
		rule := rules[i]
		out = append(out, &rule)
	}
	return out
}

// conditionsFromSchema rebuilds the availability conditions. The document keys
// them by name, so the list comes back ordered by name.
func conditionsFromSchema(conditions map[string]ConditionSchema) []Condition {
	if len(conditions) == 0 {
		return nil
	}
	out := make([]Condition, 0, len(conditions))
	for _, name := range slices.Sorted(maps.Keys(conditions)) {
		c := conditions[name]
		out = append(out, Condition{Name: name, Type: c.Type, Description: c.Description})
	}
	return out
}

// SchemaDocument renders the declaration as the v1 document. It composes
// Schemas and CommonFlagSchemas, so the per-command and aggregate renderings
// cannot disagree. Like the registry manifest, it renders without building a
// tree, so it re-checks the output declarations itself.
func (a *App) SchemaDocument() (SchemaDocument, error) {
	if len(a.Commands) == 0 {
		return SchemaDocument{}, fmt.Errorf("app %q declares no commands", a.Name)
	}
	// The tree's validate owns the declaration checks — leaf-only shapes,
	// output shapes, names, flags — so the render and the rebuild reject the
	// same declarations with the same messages.
	if err := validate(a); err != nil {
		return SchemaDocument{}, fmt.Errorf("app %q: %w", a.Name, err)
	}
	var walk func(path []string, cmd *Command) error
	walk = func(path []string, cmd *Command) error {
		for _, sub := range cmd.Subcommands {
			if err := walk(append(path, sub.Name), sub); err != nil {
				return err
			}
		}
		return nil
	}
	for _, cmd := range a.Commands {
		if err := walk([]string{cmd.Name}, cmd); err != nil {
			return SchemaDocument{}, err
		}
	}
	doc := SchemaDocument{
		Schema:              SchemaDocumentV1,
		Name:                a.Name,
		Version:             a.Version,
		Commands:            a.Schemas(),
		CommonFlags:         a.CommonFlagSchemas(),
		HandlerOwnedSignals: a.HandlerOwnedSignals,
	}
	if len(a.AvailabilityConditions) > 0 {
		doc.Conditions = make(map[string]ConditionSchema, len(a.AvailabilityConditions))
		for _, c := range a.AvailabilityConditions {
			doc.Conditions[c.Name] = ConditionSchema{Type: c.Type, Description: c.Description}
		}
	}
	// A handler-owned app chooses its own signal exit codes, so the
	// library's convention does not apply and is not claimed.
	if !a.HandlerOwnedSignals {
		doc.Signals = SignalExitCodes()
	}
	return doc, nil
}
