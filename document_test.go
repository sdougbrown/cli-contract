package clicontract

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
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
				Flags: []Flag{
					{Name: "depth", Kind: Int, Usage: "fetch depth", Default: 3},
					{Name: "timeout", Kind: Duration, Usage: "fetch timeout", Default: 5 * time.Second},
					{Name: "retries", Kind: Int, Usage: "retry count"},
					{Name: "grace", Kind: Duration, Usage: "grace period"},
					{Name: "verbose", Kind: Bool, Usage: "log requests", Default: true},
					{Name: "label", Kind: String, Usage: "request label", Default: "standard", Required: true},
					{Name: "tags", Kind: Repeat, Usage: "tags to attach", Default: []string{"draft"}},
				},
				Output: Output{Mode: OutputJSON, JSONPointer: "/item", SuccessExitCodes: []int{0}},
				Handle: handle,
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
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}

	if doc.Schema != SchemaDocumentV1 || doc.Name != "fixture" || doc.Version != "1.2.3" {
		t.Errorf("document header = %+v", doc)
	}

	t.Run("fetch", func(t *testing.T) {
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
		if got := fetch.Flags["retries"].Default; got != 0 {
			t.Errorf("defaultless Int flag rendered default %v, want 0", got)
		}
		if got := fetch.Flags["grace"].Default; got != "0s" {
			t.Errorf("defaultless Duration flag rendered default %v, want the duration string \"0s\"", got)
		}
		if fetch.Long != "Fetch one resource and print it." {
			t.Errorf("fetch long = %q", fetch.Long)
		}
		if fetch.Output == nil || fetch.Output.Mode != OutputJSON || !reflect.DeepEqual(fetch.Output.SuccessExitCodes, []int{0}) {
			t.Errorf("fetch output = %+v", fetch.Output)
		}
	})

	t.Run("edit", func(t *testing.T) {
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
	})

	t.Run("store", func(t *testing.T) {
		store := doc.Commands["store"]
		if path := store.Subcommands["path"]; path.Output != nil {
			t.Errorf("store.path output = %+v, want none for a leaf that declares none", path.Output)
		}
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
	})

	t.Run("app sections", func(t *testing.T) {
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
	})
}

// TestSchemaDocumentRejectsMalformedOutput pins the third render path: the
// document renders the declaration without building a tree, so it runs the
// same output check Run, Tree, and the manifest run.
func TestSchemaDocumentRejectsMalformedOutput(t *testing.T) {
	handle := func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
		return nil, nil
	}
	cases := []struct {
		name string
		cmd  *Command
		want string
	}{
		{
			name: "unknown mode",
			cmd:  &Command{Name: "probe", Short: "run one probe", Output: Output{Mode: "yaml"}, Handle: handle},
			want: `output mode "yaml"`,
		},
		{
			name: "mode without success codes",
			cmd:  &Command{Name: "probe", Short: "run one probe", Output: Output{Mode: OutputJSON}, Handle: handle},
			want: "without success exit codes",
		},
		{
			name: "parent declares output",
			cmd: &Command{
				Name: "store", Short: "store utilities",
				Output:      Output{Mode: OutputJSON, SuccessExitCodes: []int{0}},
				Subcommands: []*Command{{Name: "path", Short: "print the path", Handle: handle}},
			},
			want: "only a leaf command can",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&App{Name: "t", Version: "v1", RootShort: "test cli", Commands: []*Command{tc.cmd}}).SchemaDocument()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestSchemaDocumentCoversTheSpecVocabulary renders an app declaring every
// rule type, condition type, and output mode the spec's schema defines, so
// enum drift in the published schema fails the conformance suite.
func TestSchemaDocumentCoversTheSpecVocabulary(t *testing.T) {
	app := &App{
		Name:      "vocab",
		Version:   "v1",
		RootShort: "vocabulary fixture",
		AvailabilityConditions: []Condition{
			{Name: "read_only", Type: ConditionBoolean, Description: "read-only"},
			{Name: "region", Type: ConditionString, Description: "deployment region"},
			{Name: "replicas", Type: ConditionNumber, Description: "replica count"},
			{Name: "zones", Type: ConditionStrings, Description: "zones"},
			{Name: "thresholds", Type: ConditionNumbers, Description: "thresholds"},
		},
		Commands: []*Command{
			{
				Name:  "gate",
				Short: "gate the fleet",
				Availability: []*Rule{
					Disables([]string{"vocab-ops"}, When("read_only"), "read-only gates the ops tool"),
					AnyOf(
						Enabled(CondEq("region", "eu"), ""),
						Enabled(CondIn("replicas", 1, 2, 3), ""),
					),
					EitherOf("region-or-zones", map[string][]*Rule{
						"region": {Enabled(When("region"), "")},
						"zones":  {Enabled(When("zones"), "")},
					}),
				},
				Output: Output{Mode: OutputNone, SuccessExitCodes: []int{0}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return nil, nil
				},
			},
			{
				Name:  "vocab-ops",
				Short: "field operations",
				Flags: []Flag{
					{Name: "depth", Kind: Int, Usage: "depth"},
					{Name: "label", Kind: String, Usage: "label"},
					{Name: "tags", Kind: Repeat, Usage: "tags"},
				},
				InputRules: []*Rule{
					RequiresOn("depth", And(Present("depth"), Or(Eq("label", "x"), In("tags", "a", "b"))), "depth needs a label"),
					RequiresOn("label", Not(Absent("label")), "a label is required"),
					RequiresOn("tags", NotIn("tags", "banned"), "no banned tags"),
					RequiresOn("depth", Truthy("depth"), "a set depth is truthy"),
					RequiresOn("tags", Falsy("tags"), "empty tags are falsy"),
				},
				Output: Output{Mode: OutputJSON, SuccessExitCodes: []int{0}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return nil, nil
				},
			},
			{
				Name:   "render",
				Short:  "raw render",
				Output: Output{Mode: OutputRaw, SuccessExitCodes: []int{0}},
				Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
					return nil, nil
				},
			},
		},
	}
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatalf("vocabulary app rejected: %v", err)
	}
	if err := validateDocument(t, doc); err != nil {
		t.Fatalf("vocabulary document does not satisfy the spec:\n%v", err)
	}
}

// TestSchemaDocumentRejectsParentPositionals pins the render path's parent
// guard: a parent that declares positionals or no_interspersed fails the
// rendering, the same rejection the rebuild and the tree enforce.
func TestSchemaDocumentRejectsParentPositionals(t *testing.T) {
	parent := &Command{
		Name:        "store",
		Short:       "store utilities",
		Positionals: []Positional{{Name: "DIR"}},
		Subcommands: []*Command{{Name: "path", Short: "print the path"}},
	}
	_, err := (&App{Name: "t", Version: "v1", RootShort: "test cli", Commands: []*Command{parent}}).SchemaDocument()
	if err == nil || !strings.Contains(err.Error(), `command "store" owns subcommands and declares positionals`) {
		t.Fatalf("err = %v, want the parent positionals rejection", err)
	}
	strict := &Command{
		Name:           "store",
		Short:          "store utilities",
		NoInterspersed: true,
		Subcommands:    []*Command{{Name: "path", Short: "print the path"}},
	}
	_, err = (&App{Name: "t", Version: "v1", RootShort: "test cli", Commands: []*Command{strict}}).SchemaDocument()
	if err == nil || !strings.Contains(err.Error(), `command "store" owns subcommands and declares positionals`) {
		t.Fatalf("err = %v, want the parent no_interspersed rejection", err)
	}
}

// TestValidateRejectsParentPositionals pins the tree path's parent guard: the
// same declaration the document rebuild rejects cannot build a tree.
func TestValidateRejectsParentPositionals(t *testing.T) {
	parent := &Command{
		Name:        "store",
		Short:       "store utilities",
		Positionals: []Positional{{Name: "DIR"}},
		Subcommands: []*Command{{Name: "path", Short: "print the path", Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
			return nil, nil
		}}},
	}
	msg := validateMessage(&App{Name: "t", Version: "v1", RootShort: "test cli", Commands: []*Command{parent}})
	if !strings.Contains(msg, `command "store" owns subcommands and declares positionals`) {
		t.Fatalf("validate message = %q, want the parent positionals rejection", msg)
	}
}

// TestSchemaDocumentRejectsUnrenderableDeclarations pins the renderer's input
// guards: an app with no commands and a command with no name both fail the
// rendering — the latter through the tree's validate, which the renderer runs
// before rendering — instead of emitting a document the published schema
// rejects.
func TestSchemaDocumentRejectsUnrenderableDeclarations(t *testing.T) {
	empty := &App{Name: "t", Version: "v1", RootShort: "test cli"}
	if _, err := empty.SchemaDocument(); err == nil || !strings.Contains(err.Error(), "declares no commands") {
		t.Errorf("err = %v, want the no-commands rejection", err)
	}
	// The no-name rejection rides the render path now: SchemaDocument runs
	// the tree's validate before rendering, so both paths reject identically.
	named := &App{
		Name: "t", Version: "v1", RootShort: "test cli",
		Commands: []*Command{{Name: "", Short: "no name", Handle: func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
			return nil, nil
		}}},
	}
	if _, err := named.SchemaDocument(); err == nil || !strings.Contains(err.Error(), `command "" has no name`) {
		t.Errorf("err = %v, want the no-name rejection", err)
	}
}

// TestSchemasZeroesParentRules pins the direct-call guard: Schemas() validates
// nothing, so it must zero leaf-only members rather than publish them.
func TestSchemasZeroesParentRules(t *testing.T) {
	app := schemaDocumentApp()
	app.Commands[2].Availability = []*Rule{
		Enabled(Present("store-dir"), "a store needs a directory"),
	}
	app.Commands[2].InputRules = []*Rule{
		RequiresOn("depth", Present("timeout"), "a depth needs a timeout"),
	}
	schemas := app.Schemas()
	if len(schemas["store"].Availability) != 0 {
		t.Errorf("parent availability = %+v, want none on a command with subcommands", schemas["store"].Availability)
	}
	if len(schemas["store"].InputRules) != 0 {
		t.Errorf("parent input rules = %+v, want none on a command with subcommands", schemas["store"].InputRules)
	}
}
func TestDocumentAppAppliesHandWrittenDefaults(t *testing.T) {
	doc := &SchemaDocument{
		Schema: "cli-contract/cli-schema/v1",
		Commands: map[string]CommandSchema{
			"probe": {
				Short:  "probe",
				Use:    "probe",
				Output: &Output{Mode: OutputJSON, SuccessExitCodes: []int{0}},
				Flags: map[string]FlagSchema{
					"n":     {Kind: "integer", Usage: "n", Default: 3},
					"grace": {Kind: "duration", Usage: "grace", Default: 5 * time.Second},
					"tags":  {Kind: "repeat", Usage: "tags", Default: []string{"draft"}},
				},
			},
		},
	}
	app, err := doc.App(map[string]HandleFunc{"probe": func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("App rejected a hand-written int default: %v", err)
	}
	var probe *Command
	for _, cmd := range app.Commands {
		if cmd.Name == "probe" {
			probe = cmd
		}
	}
	defaults := map[string]any{}
	for _, flag := range probe.Flags {
		defaults[flag.Name] = flag.Default
	}
	if got, ok := defaults["n"].(int); !ok || got != 3 {
		t.Errorf("n default = %#v, want the declared int 3", defaults["n"])
	}
	if got, ok := defaults["grace"].(time.Duration); !ok || got != 5*time.Second {
		t.Errorf("grace default = %#v, want the declared 5s duration", defaults["grace"])
	}
	if got, ok := defaults["tags"].([]string); !ok || len(got) != 1 || got[0] != "draft" {
		t.Errorf("tags default = %#v, want the declared [draft]", defaults["tags"])
	}
}

// TestParseSchemaDocumentRejectsMalformedInput pins the decoder's early
// failures: unparseable JSON and a document with no schema identifier.
func TestParseSchemaDocumentRejectsMalformedInput(t *testing.T) {
	if _, err := ParseSchemaDocument([]byte("not json")); err == nil {
		t.Error("ParseSchemaDocument accepted unparseable JSON")
	}
	_, err := ParseSchemaDocument([]byte(`{"name": "fixture"}`))
	if err == nil || !strings.Contains(err.Error(), "no schema id") {
		t.Errorf("err = %v, want the missing schema id rejection", err)
	}
	_, err = ParseSchemaDocument([]byte(`{"schema": "cli-contract/cli-schema/v2"}`))
	if err == nil || !strings.Contains(err.Error(), "unsupported schema id") {
		t.Errorf("err = %v, want the unsupported schema id rejection", err)
	}
}

// TestSchemaDocumentAppRejectsEmptyCommands pins the empty-commands guard.
func TestSchemaDocumentAppRejectsEmptyCommands(t *testing.T) {
	empty := &SchemaDocument{Schema: "cli-contract/cli-schema/v1", Commands: map[string]CommandSchema{}}
	_, err := empty.App(nil)
	if err == nil || !strings.Contains(err.Error(), "declares no commands") {
		t.Errorf("err = %v, want the no-commands rejection", err)
	}
}

// TestSchemaDocumentDetachesFromDeclaration pins the copy outputValue makes:
// mutating the declaration after rendering must not reach the document.
func TestSchemaDocumentDetachesFromDeclaration(t *testing.T) {
	app := schemaDocumentApp()
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	app.Commands[0].Output.SuccessExitCodes[0] = 9
	if got := doc.Commands["fetch"].Output.SuccessExitCodes; !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("document success codes = %v, want the rendered [0], not the mutated declaration", got)
	}
}

// TestSchemaDocumentJSONShape pins the wire names a consumer reads, not the Go
// field names. A consumer outside Go only ever sees these.
func TestSchemaDocumentJSONShape(t *testing.T) {
	app := schemaDocumentApp()
	// One input rule so the wire name for the rule list is pinned too.
	app.Commands[0].InputRules = []*Rule{
		RequiresOn("depth", Present("timeout"), "a depth needs a timeout"),
	}
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}

	t.Run("header", func(t *testing.T) {
		for _, key := range []string{"schema", "name", "version", "commands", "common_flags", "conditions", "signals"} {
			if _, ok := wire[key]; !ok {
				t.Errorf("document has no %q key", key)
			}
		}
		if wire["schema"] != "cli-contract/cli-schema/v1" {
			t.Errorf("schema id = %v, want the v1 identifier literal", wire["schema"])
		}
		if _, ok := wire["handler_owned_signals"]; ok {
			t.Error("document declares handler_owned_signals for an app that did not")
		}
		app.CommonFlags = nil
		doc, err = app.SchemaDocument()
		if err != nil {
			t.Fatal(err)
		}
		b, err = json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var noCommon map[string]any
		if err := json.Unmarshal(b, &noCommon); err != nil {
			t.Fatal(err)
		}
		if _, ok := noCommon["common_flags"]; ok {
			t.Errorf("app with no common flags renders common_flags: %v", noCommon["common_flags"])
		}
	})

	commands, ok := wire["commands"].(map[string]any)
	if !ok {
		t.Fatal("commands is not an object")
	}

	t.Run("fetch", func(t *testing.T) {
		fetch, ok := commands["fetch"].(map[string]any)
		if !ok {
			t.Fatal("commands.fetch is not an object")
		}
		for _, key := range []string{"short", "long", "use", "url_positional", "positionals", "no_interspersed", "input_rules", "output"} {
			if _, ok := fetch[key]; !ok {
				t.Errorf("commands.fetch has no %q key", key)
			}
		}
		if _, ok := fetch["subcommands"]; ok {
			t.Error("commands.fetch renders subcommands on a leaf")
		}
		if _, ok := fetch["inputRules"]; ok {
			t.Error("commands.fetch renders the camelCase \"inputRules\" key")
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
		output, ok := fetch["output"].(map[string]any)
		if !ok {
			t.Fatal("commands.fetch.output is not an object")
		}
		for _, key := range []string{"mode", "json_pointer", "success_exit_codes"} {
			if _, ok := output[key]; !ok {
				t.Errorf("commands.fetch.output has no %q key", key)
			}
		}
	})

	t.Run("edit", func(t *testing.T) {
		edit, ok := commands["edit"].(map[string]any)
		if !ok {
			t.Fatal("commands.edit is not an object")
		}
		if _, ok := edit["availability"]; !ok {
			t.Error("commands.edit has no \"availability\" key")
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
	})

	t.Run("store", func(t *testing.T) {
		store, ok := commands["store"].(map[string]any)
		if !ok {
			t.Fatal("commands.store is not an object")
		}
		if _, ok := store["subcommands"]; !ok {
			t.Error("commands.store has no \"subcommands\" key")
		}
	})

	t.Run("conditions", func(t *testing.T) {
		conditions, ok := wire["conditions"].(map[string]any)
		if !ok {
			t.Fatal("conditions is not an object")
		}
		readOnly, ok := conditions["read_only"].(map[string]any)
		if !ok {
			t.Fatal("conditions.read_only is not an object")
		}
		for _, key := range []string{"type", "description"} {
			if _, ok := readOnly[key]; !ok {
				t.Errorf("conditions.read_only has no %q key", key)
			}
		}
	})
}

func TestSchemaDocumentHandlerOwnedSignals(t *testing.T) {
	app := schemaDocumentApp()
	app.HandlerOwnedSignals = true
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}

	if doc.Signals != nil {
		t.Errorf("signals = %v, want none: the app chooses its own codes", doc.Signals)
	}
	if !doc.HandlerOwnedSignals {
		t.Error("handler_owned_signals not rendered")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if got, ok := wire["handler_owned_signals"]; !ok || got != true {
		t.Errorf("handler_owned_signals = %v (present: %v), want true", got, ok)
	}
	if _, ok := wire["signals"]; ok {
		t.Error("handler-owned app renders the library's signal convention")
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
		{
			name: "success code above 255",
			cmd:  leaf(Output{Mode: OutputNone, SuccessExitCodes: []int{300}}),
			want: `command "probe" declares success exit code 300, want a code in [0, 255]`,
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

// docHandlers maps every leaf of schemaDocumentApp to a handler, the way a
// fixture runner supplies them.
func docHandlers() map[string]HandleFunc {
	handle := func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
		return map[string]any{
			"command": inv.Command,
			"path":    inv.Path,
			"args":    inv.Args,
			"depth":   inv.Ints["depth"],
			"timeout": inv.Durations["timeout"].String(),
		}, nil
	}
	return map[string]HandleFunc{
		"fetch":      handle,
		"edit":       handle,
		"store path": handle,
	}
}

// TestSchemaDocumentRoundTripsThroughApp is the document's contract with a
// runner: a document must describe enough to rebuild the app that rendered it.
func TestSchemaDocumentRoundTripsThroughApp(t *testing.T) {
	app := schemaDocumentApp()
	// A handler-owned app must rebuild with the same convention: the apply-side
	// copy of HandlerOwnedSignals is pinned by the round trip.
	app.HandlerOwnedSignals = true
	doc, err := app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSchemaDocument(b)
	if err != nil {
		t.Fatal(err)
	}
	app, err2 := parsed.App(docHandlers())
	if err2 != nil {
		t.Fatal(err2)
	}
	if !app.HandlerOwnedSignals {
		t.Error("rebuilt app lost the handler-owned signals declaration")
	}
	got, err := app.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Errorf("document changed across the round trip:\n got %+v\nwant %+v", got, doc)
	}
}

// TestDocumentAppRunsTheDeclaredSurface proves the rebuilt app is runnable,
// not merely structurally equal: parsing, defaults, and nested handler keys
// all come from the document.
func TestDocumentAppRunsTheDeclaredSurface(t *testing.T) {
	doc, err := schemaDocumentApp().SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	app, err := doc.App(docHandlers())
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	adapter := &testAdapter{}
	if code := Run(app, adapter, []string{"fetch", "--depth", "7", "--label", "standard", "u"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	got := adapter.wrote[0].(map[string]any)
	if got["args"].([]string)[0] != "u" {
		t.Errorf("args = %v", got["args"])
	}
	if got["depth"] != 7 {
		t.Errorf("depth = %v, want the typed flag value", got["depth"])
	}
	if got["timeout"] != "5s" {
		t.Errorf("timeout = %v, want the document's default", got["timeout"])
	}

	adapter = &testAdapter{}
	if code := Run(app, adapter, []string{"store", "path"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("store path code = %d, stderr = %q", code, stderr.String())
	}
	if got := adapter.wrote[0].(map[string]any); got["command"] != "store" {
		t.Errorf("nested handler saw %v", got["command"])
	}
}

func TestDocumentAppRequiresLeafHandlers(t *testing.T) {
	doc, err := schemaDocumentApp().SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	handlers := docHandlers()
	delete(handlers, "store path")
	_, err = doc.App(handlers)
	if err == nil || !strings.Contains(err.Error(), `command "store path" declares no handler`) {
		t.Fatalf("err = %v, want the missing handler named", err)
	}
}

func TestDocumentAppRejectsURLMismatch(t *testing.T) {
	doc, err := schemaDocumentApp().SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	fetch := doc.Commands["fetch"]
	fetch.URL = false
	doc.Commands["fetch"] = fetch
	_, err = doc.App(docHandlers())
	if err == nil || !strings.Contains(err.Error(), `command "fetch" declares url_positional false but its positionals say true`) {
		t.Fatalf("err = %v, want the inconsistency named", err)
	}
	fetch = doc.Commands["fetch"]
	fetch.URL = true
	fetch.Positionals = nil
	doc.Commands["fetch"] = fetch
	_, err = doc.App(docHandlers())
	if err == nil || !strings.Contains(err.Error(), `command "fetch" declares url_positional true but its positionals say false`) {
		t.Fatalf("err = %v, want the reverse inconsistency named", err)
	}
}

// TestDocumentAppRejectsUnappliableDeclarations keeps App honest where the
// JSON Schema cannot: a hand-written document may be shaped correctly and
// still describe a parser it cannot build.
func TestDocumentAppRejectsUnappliableDeclarations(t *testing.T) {
	handle := func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
		return nil, nil
	}
	leaf := func(flags map[string]FlagSchema) map[string]CommandSchema {
		return map[string]CommandSchema{
			"probe": {Short: "probe", Use: "probe", Flags: flags, Output: &Output{Mode: OutputJSON, SuccessExitCodes: []int{0}}},
		}
	}
	cases := []struct {
		name     string
		commands map[string]CommandSchema
		want     string
	}{
		{
			name:     "integer default rendered as a string",
			commands: leaf(map[string]FlagSchema{"n": {Kind: "integer", Usage: "count", Default: "3"}}),
			want:     "integer default 3 is not a number",
		},
		{
			name:     "unparseable duration default",
			commands: leaf(map[string]FlagSchema{"wait": {Kind: "duration", Usage: "wait", Default: "5x"}}),
			want:     `duration default "5x"`,
		},
		{
			name:     "bool default as a string",
			commands: leaf(map[string]FlagSchema{"v": {Kind: "bool", Usage: "v", Default: "yes"}}),
			want:     `bool default yes is not a boolean`,
		},
		{
			name:     "string default as a number",
			commands: leaf(map[string]FlagSchema{"l": {Kind: "string", Usage: "l", Default: 42.0}}),
			want:     `string default 42 is not a string`,
		},
		{
			name:     "repeat default with a non-string element",
			commands: leaf(map[string]FlagSchema{"t": {Kind: "repeat", Usage: "t", Default: []any{"a", 1}}}),
			want:     `repeat default [a 1] is not an array of strings`,
		},
		{
			name:     "repeat default as a scalar",
			commands: leaf(map[string]FlagSchema{"t": {Kind: "repeat", Usage: "t", Default: "x"}}),
			want:     `repeat default x is not an array`,
		},
		{
			name:     "out-of-range integer default",
			commands: leaf(map[string]FlagSchema{"n": {Kind: "integer", Usage: "n", Default: 1.18e21}}),
			want:     `integer default 1.18e+21 is out of int range`,
		},
		{
			name:     "numeric duration default is rejected",
			commands: leaf(map[string]FlagSchema{"d": {Kind: "duration", Usage: "d", Default: 5.0}}),
			want:     `duration default 5 is not a duration string`,
		},
		{
			name:     "fractional integer default",
			commands: leaf(map[string]FlagSchema{"n": {Kind: "integer", Usage: "n", Default: 3.5}}),
			want:     `integer default 3.5 is not a whole number`,
		},
		{
			name:     "unknown kind",
			commands: leaf(map[string]FlagSchema{"n": {Kind: "float", Usage: "n", Default: 1.0}}),
			want:     `unknown kind "float"`,
		},
		{
			name: "leaf without help short",
			commands: map[string]CommandSchema{
				"probe": {Use: "probe", Output: &Output{Mode: OutputJSON, SuccessExitCodes: []int{0}}},
			},
			want: `command "probe" has no help short`,
		},
		{
			name: "parent declares rules",
			commands: map[string]CommandSchema{
				"store": {
					Short:        "store utilities",
					Use:          "store",
					Availability: []Rule{{Type: "enabledWhen", Reason: "the profile is read-only"}},
					Subcommands: map[string]CommandSchema{
						"path": {Short: "print the path", Use: "path"},
					},
				},
			},
			want: `command "store" declares rules but owns subcommands`,
		},
		{
			name: "parent declares only success codes",
			commands: map[string]CommandSchema{
				"store": {
					Short:  "store utilities",
					Use:    "store",
					Output: &Output{SuccessExitCodes: []int{0}},
					Subcommands: map[string]CommandSchema{
						"path": {Short: "print the path", Use: "path"},
					},
				},
			},
			want: `command "store" declares output, but only a leaf command can`,
		},
		{
			name: "leaf mode without success codes",
			commands: map[string]CommandSchema{
				"probe": {Short: "run one probe", Use: "probe", Output: &Output{Mode: OutputJSON}},
			},
			want: `command "probe" declares output mode "json" without success exit codes`,
		},
		{
			name: "parent declares positionals",
			commands: map[string]CommandSchema{
				"store": {
					Short:       "store utilities",
					Use:         "store [DIR]",
					URL:         false,
					Positionals: []Positional{{Name: "DIR"}},
					Subcommands: map[string]CommandSchema{
						"path": {Short: "print the path", Use: "path", Output: &Output{Mode: OutputJSON, SuccessExitCodes: []int{0}}},
					},
				},
			},
			want: `command "store" owns subcommands and declares positionals`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := &SchemaDocument{Schema: "test/cli-schema/v1", Name: "fixture", Commands: tc.commands}
			_, err := doc.App(map[string]HandleFunc{
				"probe":      handle,
				"store path": handle,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
