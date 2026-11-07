package clicontract

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

// schemaDocumentApp is a declaration that exercises every member the document
// renders: positionals, a long help, single-dash parsing, availability, and a
// per-command output block.
func schemaDocumentApp() *App {
	handle := func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
		return map[string]any{"command": inv.Command}, nil
	}
	return &App{
		Name:      "fixture",
		Version:   "1.2.3",
		RootShort: "fixture does one thing",
		AvailabilityConditions: []Condition{
			{Name: "read_only", Type: ConditionBoolean, Description: "the profile is read-only"},
		},
		Commands: []*Command{
			{
				Name:           "fetch",
				Short:          "fetch a resource",
				Long:           "Fetch one resource and print it.",
				Positionals:    []Positional{{Name: "URL", Required: true}},
				NoInterspersed: true,
				Flags:          []Flag{{Name: "depth", Kind: Int, Usage: "fetch depth", Default: 3}},
				Output:         Output{Mode: OutputJSON, JSONPointer: "/item", SuccessExitCodes: []int{0}},
				Handle:         handle,
			},
			{
				Name:        "edit",
				Short:       "edit a thing",
				Positionals: []Positional{{Name: "files", Variadic: true}},
				Availability: []*Rule{
					Enabled(Not(When("read_only")), "the profile is read-only"),
				},
				Output: Output{Mode: OutputRaw, SuccessExitCodes: []int{0}},
				Handle: handle,
			},
			{
				Name:            "store",
				Short:           "store utilities",
				NoInterspersed:  true,
				PersistentFlags: []Flag{{Name: "store-dir", Kind: String, Usage: "store directory"}},
				Subcommands: []*Command{
					{Name: "path", Short: "print the store path", Handle: handle},
				},
			},
		},
		CommonFlags: []Flag{{Name: "json", Kind: Bool, Usage: "no-op; output is already JSON"}},
	}
}

func TestSchemaDocumentRendersTheSurface(t *testing.T) {
	app := schemaDocumentApp()
	doc := app.SchemaDocument()

	if doc.Schema != SchemaDocumentV1 || doc.Name != "fixture" || doc.Version != "1.2.3" {
		t.Errorf("document header = %+v", doc)
	}

	fetch := doc.Commands["fetch"]
	if len(fetch.Positionals) != 1 || fetch.Positionals[0].Name != "URL" || !fetch.Positionals[0].Required {
		t.Errorf("fetch positionals = %+v", fetch.Positionals)
	}
	if !fetch.URL {
		t.Error("fetch is not marked as taking a URL positional")
	}
	if !fetch.NoInterspersed {
		t.Error("fetch lost its no_interspersed declaration")
	}
	if fetch.Long != "Fetch one resource and print it." {
		t.Errorf("fetch long = %q", fetch.Long)
	}
	if fetch.Output == nil || fetch.Output.Mode != OutputJSON || !reflect.DeepEqual(fetch.Output.SuccessExitCodes, []int{0}) {
		t.Errorf("fetch output = %+v", fetch.Output)
	}

	edit := doc.Commands["edit"]
	if len(edit.Availability) != 1 || edit.Availability[0].Type != "enabledWhen" {
		t.Errorf("edit availability = %+v", edit.Availability)
	}
	if len(edit.Positionals) != 1 || edit.Positionals[0].Name != "files" || !edit.Positionals[0].Variadic {
		t.Errorf("edit positionals = %+v", edit.Positionals)
	}
	if edit.Output == nil || edit.Output.Mode != OutputRaw || !reflect.DeepEqual(edit.Output.SuccessExitCodes, []int{0}) {
		t.Errorf("edit output = %+v", edit.Output)
	}

	if path := doc.Commands["store"].Subcommands["path"]; path.Output != nil {
		t.Errorf("store.path output = %+v, want none for a leaf that declares none", path.Output)
	}
	store := doc.Commands["store"]
	if store.Output != nil {
		t.Errorf("store output = %+v, want none on a command with subcommands", store.Output)
	}
	if store.Positionals != nil {
		t.Errorf("store positionals = %+v, want none on a command with subcommands", store.Positionals)
	}
	if store.NoInterspersed {
		t.Error("store renders no_interspersed on a command with subcommands")
	}
	if _, ok := store.Flags["store-dir"]; !ok {
		t.Error("store's persistent flag is missing from its entry")
	}

	if got := doc.Conditions["read_only"]; got.Type != ConditionBoolean || got.Description == "" {
		t.Errorf("read_only condition = %+v", got)
	}
	if doc.Signals["SIGINT"] != 130 || doc.Signals["SIGTERM"] != 143 {
		t.Errorf("signals = %v", doc.Signals)
	}
	if doc.HandlerOwnedSignals {
		t.Error("document claims handler-owned signals for an app that did not declare them")
	}
	if doc.ExitCodes != nil {
		t.Errorf("exit codes = %v, want none: only the adapter knows them", doc.ExitCodes)
	}
}

// TestSchemaDocumentJSONShape pins the wire names a consumer reads, not the Go
// field names. A consumer outside Go only ever sees these.
func TestSchemaDocumentJSONShape(t *testing.T) {
	app := schemaDocumentApp()
	// One input rule so the wire name for the rule list is pinned too.
	app.Commands[0].InputRules = []*Rule{
		RequiresOn("depth", Present("URL"), "a depth needs a URL"),
	}
	b, err := json.Marshal(app.SchemaDocument())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema", "name", "version", "commands", "common_flags", "conditions", "signals"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("document has no %q key", key)
		}
	}
	if doc["schema"] != "cli-contract/cli-schema/v1" {
		t.Errorf("schema id = %v, want the v1 identifier literal", doc["schema"])
	}
	if _, ok := doc["handler_owned_signals"]; ok {
		t.Error("document declares handler_owned_signals for an app that did not")
	}

	commands, ok := doc["commands"].(map[string]any)
	if !ok {
		t.Fatal("commands is not an object")
	}
	fetch, ok := commands["fetch"].(map[string]any)
	if !ok {
		t.Fatal("commands.fetch is not an object")
	}
	for _, key := range []string{"positionals", "no_interspersed", "input_rules", "output"} {
		if _, ok := fetch[key]; !ok {
			t.Errorf("commands.fetch has no %q key", key)
		}
	}
	if _, ok := fetch["inputRules"]; ok {
		t.Error("commands.fetch renders the camelCase \"inputRules\" key")
	}
	edit, ok := commands["edit"].(map[string]any)
	if !ok {
		t.Fatal("commands.edit is not an object")
	}
	if _, ok := edit["availability"]; !ok {
		t.Error("commands.edit has no \"availability\" key")
	}
	positionals, ok := fetch["positionals"].([]any)
	if !ok || len(positionals) != 1 {
		t.Fatalf("commands.fetch positionals = %#v", fetch["positionals"])
	}
	urlPos, ok := positionals[0].(map[string]any)
	if !ok {
		t.Fatal("commands.fetch.positionals[0] is not an object")
	}
	if urlPos["name"] != "URL" || urlPos["required"] != true {
		t.Errorf("commands.fetch.positionals[0] = %#v, want name URL, required true", urlPos)
	}
	if _, ok := urlPos["variadic"]; ok {
		t.Error("commands.fetch.positionals[0] renders variadic on a non-variadic positional")
	}
	editPositionals, ok := edit["positionals"].([]any)
	if !ok || len(editPositionals) != 1 {
		t.Fatalf("commands.edit positionals = %#v", edit["positionals"])
	}
	files, ok := editPositionals[0].(map[string]any)
	if !ok {
		t.Fatal("commands.edit.positionals[0] is not an object")
	}
	if files["name"] != "files" || files["variadic"] != true {
		t.Errorf("commands.edit.positionals[0] = %#v, want name files, variadic true", files)
	}
	if _, ok := files["required"]; ok {
		t.Error("commands.edit.positionals[0] renders required on an optional positional")
	}
	output, ok := fetch["output"].(map[string]any)
	if !ok {
		t.Fatal("commands.fetch.output is not an object")
	}
	for _, key := range []string{"mode", "json_pointer", "success_exit_codes"} {
		if _, ok := output[key]; !ok {
			t.Errorf("commands.fetch.output has no %q key", key)
		}
	}
}

func TestSchemaDocumentHandlerOwnedSignals(t *testing.T) {
	app := schemaDocumentApp()
	app.HandlerOwnedSignals = true
	doc := app.SchemaDocument()

	if doc.Signals != nil {
		t.Errorf("signals = %v, want none: the app chooses its own codes", doc.Signals)
	}
	if !doc.HandlerOwnedSignals {
		t.Error("handler_owned_signals not rendered")
	}
}

// validateMessage builds the app's tree and returns validate's panic message
// when the declaration is rejected.
func validateMessage(app *App) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg, _ = r.(string)
		}
	}()
	app.Tree()
	return ""
}

func TestValidateRejectsOutputDeclarations(t *testing.T) {
	handle := func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
		return nil, nil
	}
	leaf := func(o Output) *Command {
		return &Command{Name: "probe", Short: "run one probe", Output: o, Handle: handle}
	}
	parent := func(o Output) *Command {
		return &Command{
			Name: "store", Short: "store utilities", Output: o,
			Subcommands: []*Command{{Name: "path", Short: "print the path", Handle: handle}},
		}
	}
	cases := []struct {
		name string
		cmd  *Command
		want string
	}{
		{
			name: "parent declares output",
			cmd:  parent(Output{Mode: OutputJSON}),
			want: `command "store" declares output, but only a leaf command can`,
		},
		{
			name: "details without a mode",
			cmd:  leaf(Output{SuccessExitCodes: []int{0}}),
			want: `command "probe" declares output details without a mode`,
		},
		{
			name: "unknown mode",
			cmd:  leaf(Output{Mode: "yaml"}),
			want: `command "probe" output mode "yaml"`,
		},
		{
			name: "pointer with raw output",
			cmd:  leaf(Output{Mode: OutputRaw, JSONPointer: "/status"}),
			want: `command "probe" declares a json pointer with output mode "raw"`,
		},
		{
			name: "malformed pointer",
			cmd:  leaf(Output{Mode: OutputJSON, JSONPointer: "status"}),
			want: `command "probe" json pointer "status"`,
		},
		{
			name: "negative success code",
			cmd:  leaf(Output{Mode: OutputNone, SuccessExitCodes: []int{-1}}),
			want: `command "probe" declares success exit code -1`,
		},
		{
			name: "repeated success code",
			cmd:  leaf(Output{Mode: OutputNone, SuccessExitCodes: []int{0, 0}}),
			want: `command "probe" declares success exit code 0 twice`,
		},
		{
			name: "mode without success codes",
			cmd:  leaf(Output{Mode: OutputJSON}),
			want: `command "probe" declares output mode "json" without success exit codes`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateMessage(&App{Name: "t", Commands: []*Command{tc.cmd}})
			if !strings.Contains(msg, tc.want) {
				t.Errorf("validate message = %q, want it to contain %q", msg, tc.want)
			}
		})
	}
}
