package clicontract

import "strings"

// The schema document: the declared command surface and the behavior the
// library fixes, rendered as one machine-readable value. Apps encode it and
// add their own facts (statuses, error codes, envelope fields, exit codes)
// around it; the cli-contract specification describes the whole document.

// SchemaDocumentV1 identifies the document shape this library renders. The
// spec calls this shape v1; a document that changes incompatibly gets a new
// id.
const SchemaDocumentV1 = "cli-contract/cli-schema/v1"

// ConditionSchema describes one availability input. The declaration's Name is
// the map key, so it is not repeated here.
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

// SchemaDocument renders the declaration as the v1 document. It composes
// Schemas and CommonFlagSchemas, so the per-command and aggregate renderings
// cannot disagree. Like the registry manifest, it renders without building a
// tree, so it re-checks the output declarations itself.
func (a *App) SchemaDocument() (SchemaDocument, error) {
	var walk func(path []string, cmd *Command) error
	walk = func(path []string, cmd *Command) error {
		if err := cmd.ValidateOutput(strings.Join(path, " ")); err != nil {
			return err
		}
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
