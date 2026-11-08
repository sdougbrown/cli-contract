package clicontract

// The reference conformance runner: it reads the spec's fixtures, rebuilds the
// app each document declares, and runs the cases in process. The fixture format
// and the fixture conventions are specified in spec/v1-discrepancy-ledger.md
// ("Fixture conventions"); the stream-class vocabulary (empty, json, raw, none)
// is defined by the classes below and asserted in the spec's own JSON Schema
// where the document constrains it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/sdougbrown/cli-contract/internal/envelope"
	"github.com/sdougbrown/cli-contract/internal/streamclass"
)

type conformanceIndex struct {
	FixtureVersion int `json:"fixtureVersion"`
	Fixtures       []struct {
		ID          string `json:"id"`
		Path        string `json:"path"`
		Description string `json:"description"`
	} `json:"fixtures"`
}

type conformanceFixture struct {
	FixtureVersion int               `json:"fixtureVersion"`
	ID             string            `json:"id"`
	Description    string            `json:"description"`
	Document       json.RawMessage   `json:"document"`
	Cases          []conformanceCase `json:"cases"`
}

type conformanceCase struct {
	ID     string   `json:"id"`
	Argv   []string `json:"argv"`
	Signal string   `json:"signal,omitempty"`
	Expect struct {
		Exit   int          `json:"exit"`
		Stdout *classExpect `json:"stdout,omitempty"`
		Stderr *classExpect `json:"stderr,omitempty"`
	} `json:"expect"`
}

// classExpect is one stream expectation. The class names the shape; text,
// pointer, and equals sharpen it for the prefix, json-matches, and exact
// plain-text cases.
type classExpect struct {
	Class   string `json:"class"`
	Text    string `json:"text,omitempty"`
	Pointer string `json:"pointer,omitempty"`
	Equals  any    `json:"equals,omitempty"`
}

// conformanceAdapter is the fixture app's adapter: it writes stdout as the
// document declares, classifies caller errors as usage, and injects a case's
// signal through the test seam.
type conformanceAdapter struct {
	modes  map[string]OutputMode
	inv    Invocation
	signal os.Signal
}

func (a *conformanceAdapter) ErrorResult(command string, err error) any {
	code := "internal"
	var usage *UsageError
	if errors.As(err, &usage) {
		code = "usage"
	}
	return envelope.Error(command, code, err)
}

func (a *conformanceAdapter) Write(w io.Writer, result any) error {
	switch a.mode() {
	case OutputRaw:
		text, _ := result.(string)
		_, err := io.WriteString(w, text)
		return err
	case OutputNone:
		return nil
	}
	if result == nil {
		return nil
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// mode reports the current command's declared output mode. A dispatch failure
// never reaches Deadline, so no invocation is recorded and the error envelope
// is written as JSON, the fixture app's own convention.
func (a *conformanceAdapter) mode() OutputMode {
	return a.modes[invocationPath(a.inv)]
}

func (a *conformanceAdapter) ExitCode(inv Invocation, result any, handlerErr error) int {
	if handlerErr == nil {
		return ExitOK
	}
	var usage *UsageError
	if errors.As(handlerErr, &usage) {
		return 2
	}
	return ExitOperational
}

func (a *conformanceAdapter) Deadline(ctx context.Context, inv Invocation) (context.Context, context.CancelFunc) {
	a.inv = inv
	return ctx, func() {}
}

func (a *conformanceAdapter) BeforeHandler(inv Invocation, stderr io.Writer) {}

func (a *conformanceAdapter) Validate(inv Invocation) error { return nil }

func (a *conformanceAdapter) BeforeWrite() {}

func (a *conformanceAdapter) PendingSignal() (os.Signal, bool) {
	return a.signal, a.signal != nil
}

var _ Adapter = (*conformanceAdapter)(nil)
var _ TestSeams = (*conformanceAdapter)(nil)

// TestConformanceAdapterEnvelope pins the fixture runner's failure envelope to
// the shared builder; the checker's own test pins its adapter the same way, so
// an envelope change cannot reach one producer and miss the other.
func TestConformanceAdapterEnvelope(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "usage", err: &UsageError{Message: "bad schema file"}, want: "usage"},
		{name: "internal", err: errors.New("boom"), want: "internal"},
	}
	for _, c := range cases {
		result := (&conformanceAdapter{}).ErrorResult("probe", c.err)
		got, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("%s: ErrorResult returned %T, want map[string]any", c.name, result)
		}
		if want := envelope.Error("probe", c.want, c.err); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: envelope = %#v, want %#v", c.name, got, want)
		}
	}
}

// invocationPath is the full command path of one invocation, the key both the
// document and the handler map use.
func invocationPath(inv Invocation) string {
	if inv.Command == "" {
		return ""
	}
	if len(inv.Path) == 0 {
		return inv.Command
	}
	return inv.Command + " " + strings.Join(inv.Path, " ")
}

// conformanceModes indexes every leaf's declared output mode by command path.
func conformanceModes(doc *SchemaDocument) map[string]OutputMode {
	modes := map[string]OutputMode{}
	walkLeaves(doc.Commands, "", func(path string, cmd CommandSchema) {
		if cmd.Output != nil {
			modes[path] = cmd.Output.Mode
		}
	})
	return modes
}

// conformanceHandlers builds one echo handler per leaf, honoring the leaf's
// declared output mode: a raw command writes text, a none command writes
// nothing, and a result command echoes what it parsed so a case can assert it.
func conformanceHandlers(doc *SchemaDocument) map[string]HandleFunc {
	handlers := map[string]HandleFunc{}
	walkLeaves(doc.Commands, "", func(path string, cmd CommandSchema) {
		mode := OutputJSON
		if cmd.Output != nil {
			mode = cmd.Output.Mode
		}
		handlers[path] = func(ctx context.Context, inv Invocation, _ io.Writer) (any, error) {
			switch mode {
			case OutputRaw:
				return "fixture raw output\n", nil
			case OutputNone:
				return nil, nil
			}
			return map[string]any{
				"command":  inv.Command,
				"path":     inv.Path,
				"args":     inv.Args,
				"flags":    inv.Flags,
				"bools":    inv.Bools,
				"ints":     inv.Ints,
				"supplied": inv.Supplied,
			}, nil
		}
	})
	return handlers
}

// walkLeaves visits every leaf command in name order with its full path.
func walkLeaves(commands map[string]CommandSchema, prefix string, visit func(path string, cmd CommandSchema)) {
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		cmd := commands[name]
		path := name
		if prefix != "" {
			path = prefix + " " + name
		}
		if len(cmd.Subcommands) > 0 {
			walkLeaves(cmd.Subcommands, path, visit)
			continue
		}
		visit(path, cmd)
	}
}

// TestConformanceFixtures runs every fixture in the published index.
func TestConformanceFixtures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("spec", "index.json"))
	if err != nil {
		t.Fatalf("read spec index: %v", err)
	}
	var index conformanceIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("decode spec index: %v", err)
	}
	if index.FixtureVersion != 1 {
		t.Fatalf("fixture index version = %d, want 1", index.FixtureVersion)
	}
	if len(index.Fixtures) == 0 {
		t.Fatal("the spec index lists no fixtures")
	}
	for _, entry := range index.Fixtures {
		t.Run(entry.ID, func(t *testing.T) {
			path := filepath.Join("spec", filepath.FromSlash(entry.Path))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var fixture conformanceFixture
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			if fixture.FixtureVersion != 1 {
				t.Fatalf("fixture version = %d, want 1", fixture.FixtureVersion)
			}
			if fixture.ID != entry.ID {
				t.Fatalf("fixture id = %q, index says %q", fixture.ID, entry.ID)
			}
			// The declaration inside a fixture is a schema document, so the
			// published schema validates it before anything runs.
			if err := validateDocument(t, fixture.Document); err != nil {
				t.Fatalf("fixture document does not satisfy the spec:\n%v", err)
			}
			doc, err := ParseSchemaDocument(fixture.Document)
			if err != nil {
				t.Fatalf("parse fixture document: %v", err)
			}
			app, err := doc.App(conformanceHandlers(doc))
			if err != nil {
				t.Fatalf("build fixture app: %v", err)
			}
			modes := conformanceModes(doc)
			for _, c := range fixture.Cases {
				t.Run(c.ID, func(t *testing.T) {
					runConformanceCase(t, app, modes, c)
				})
			}
		})
	}
}

// runConformanceCase executes one case and asserts its exit code and stream
// classes.
func runConformanceCase(t *testing.T, app *App, modes map[string]OutputMode, c conformanceCase) {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	signal, ok := signalByName(c.Signal)
	if c.Signal != "" && !ok {
		t.Fatalf("case %q names an unknown signal %q: the runner maps only SIGINT and SIGTERM", c.ID, c.Signal)
	}
	adapter := &conformanceAdapter{modes: modes, signal: signal}

	code := Run(app, adapter, c.Argv, stdout, stderr)
	if code != c.Expect.Exit {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, c.Expect.Exit, stdout, stderr)
	}
	assertClass(t, "stdout", stdout.String(), c.Expect.Stdout)
	assertClass(t, "stderr", stderr.String(), c.Expect.Stderr)
}

// signalByName maps a fixture's signal name to the process signal the library
// watches for.
func signalByName(name string) (os.Signal, bool) {
	switch name {
	case "SIGINT":
		return os.Interrupt, true
	case "SIGTERM":
		return syscall.SIGTERM, true
	default:
		return nil, false
	}
}

// assertClass checks one stream against its expected shape. A nil expectation
// skips the stream: a signal case, for example, asserts only the exit code.
func assertClass(t *testing.T, stream, got string, want *classExpect) {
	t.Helper()
	if want == nil {
		return
	}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("%s: %s\nwant %+v", stream, fmt.Sprintf(format, args...), *want)
	}
	switch want.Class {
	case streamclass.Empty, streamclass.NonEmpty, streamclass.PlainText, streamclass.JSONValue:
		if reason := streamclass.Judge(want.Class, got); !reason.OK() {
			fail("%s", reason)
		}
		if want.Class == streamclass.PlainText {
			if text, ok := want.Equals.(string); ok && got != text {
				fail("text = %q, want %q", got, text)
			}
		}
	case "prefix":
		if !strings.HasPrefix(got, want.Text) {
			fail("missing prefix %q", want.Text)
		}
	case "json-matches":
		value, ok := streamclass.DecodeOneJSONValue(got)
		if !ok {
			fail("not one JSON value")
		}
		resolved, ok := resolveJSONPointer(value, want.Pointer)
		if !ok {
			fail("no value at %s", want.Pointer)
		}
		if !reflect.DeepEqual(resolved, want.Equals) {
			fail("%s = %#v, want %#v", want.Pointer, resolved, want.Equals)
		}
	default:
		fail("unknown class %q", want.Class)
	}
}

// resolveJSONPointer resolves an RFC 6901 pointer against a decoded JSON value.
func resolveJSONPointer(value any, pointer string) (any, bool) {
	if pointer == "" {
		return value, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := value
	for _, token := range strings.Split(pointer[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			current = node[i]
		default:
			return nil, false
		}
	}
	return current, true
}
