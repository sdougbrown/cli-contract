// Package conformance loads the spec's conformance fixtures and runs them
// against the library: the reference runner for the CLI behavior contract.
//
// A fixture declares a CLI as data; BuildApp maps that declaration onto a
// clicontract.App whose handlers follow the handler contract documented in
// spec/conformance/README.md. CheckFixture builds the app, runs every case,
// and reports the cases whose exit code, stream classes, or pinned pointer
// values disagree.
package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	clicontract "github.com/sdougbrown/cli-contract"
)

// Index is the conformance index.json.
type Index struct {
	FixtureVersion int          `json:"fixtureVersion"`
	Fixtures       []IndexEntry `json:"fixtures"`
}

// IndexEntry names one fixture file, relative to the index.
type IndexEntry struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

// Fixture is one conformance fixture: a declaration document plus the
// cases a conforming CLI must satisfy.
type Fixture struct {
	FixtureVersion int          `json:"fixtureVersion"`
	ID             string       `json:"id"`
	Description    string       `json:"description"`
	Declaration    *Declaration `json:"declaration"`
	Cases          []Case       `json:"cases"`
}

// Case is one invocation and the expected observables.
type Case struct {
	ID               string         `json:"id"`
	Argv             []string       `json:"argv"`
	Env              map[string]string `json:"env,omitempty"`
	Signal           string         `json:"signal,omitempty"`
	ExpectedExitCode int            `json:"expectedExitCode"`
	Stdout           string         `json:"stdout"`
	StdoutMatches    map[string]any `json:"stdoutMatches,omitempty"`
	Stderr           string         `json:"stderr"`
}

// LoadIndex reads index.json and every fixture it names. Paths resolve
// against the directory containing the index.
func LoadIndex(dir string) ([]Fixture, error) {
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("index.json: %w", err)
	}
	if idx.FixtureVersion != 1 {
		return nil, fmt.Errorf("index.json: fixtureVersion %d, want 1", idx.FixtureVersion)
	}
	var out []Fixture
	for _, entry := range idx.Fixtures {
		fb, err := os.ReadFile(filepath.Join(dir, entry.Path))
		if err != nil {
			return nil, err
		}
		var f Fixture
		if err := json.Unmarshal(fb, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Path, err)
		}
		if f.FixtureVersion != 1 {
			return nil, fmt.Errorf("%s: fixtureVersion %d, want 1", entry.Path, f.FixtureVersion)
		}
		out = append(out, f)
	}
	return out, nil
}

// Outcome is what one run observed.
type Outcome struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Failure names one violated case and what disagreed.
type Failure struct {
	Case    string
	Details string
}

// Check compares one observed outcome against the case and returns the
// disagreements (empty when the case holds). It is shared by the fixture
// runner and the black-box checker.
func Check(c Case, obs Outcome) []string {
	var problems []string
	if obs.ExitCode != c.ExpectedExitCode {
		problems = append(problems, fmt.Sprintf("exit code = %d, want %d", obs.ExitCode, c.ExpectedExitCode))
	}
	if err := matchClass("stdout", c.Stdout, obs.Stdout); err != nil {
		problems = append(problems, err.Error())
	}
	if err := matchClass("stderr", c.Stderr, obs.Stderr); err != nil {
		problems = append(problems, err.Error())
	}
	if len(c.StdoutMatches) > 0 {
		var doc any
		if err := json.Unmarshal([]byte(obs.Stdout), &doc); err != nil {
			problems = append(problems, fmt.Sprintf("stdout is not one JSON value for stdoutMatches: %v", err))
		} else {
			pointers := make([]string, 0, len(c.StdoutMatches))
			for p := range c.StdoutMatches {
				pointers = append(pointers, p)
			}
			sort.Strings(pointers)
			for _, p := range pointers {
				got, ok := resolvePointer(doc, p)
				if !ok {
					problems = append(problems, fmt.Sprintf("stdout pointer %q absent", p))
					continue
				}
				if fmt.Sprint(got) != fmt.Sprint(c.StdoutMatches[p]) {
					problems = append(problems, fmt.Sprintf("stdout pointer %q = %v, want %v", p, got, c.StdoutMatches[p]))
				}
			}
		}
	}
	return problems
}

// CheckFixture runs every case of the fixture and returns the failures.
func CheckFixture(f *Fixture) []Failure {
	app, adapter := BuildApp(f.Declaration)
	var failures []Failure
	for _, c := range f.Cases {
		obs := runCase(app, adapter, c)
		if problems := Check(c, obs); problems != nil {
			failures = append(failures, Failure{Case: c.ID, Details: strings.Join(problems, "; ")})
		}
	}
	return failures
}

// runCase runs one case against the built app with fresh buffers.
func runCase(app *clicontract.App, adapter *Adapter, c Case) Outcome {
	for _, k := range envKeys(c.Env) {
		os.Setenv(k, c.Env[k])
		defer os.Unsetenv(k)
	}
	adapter.signal = c.Signal
	var stdout, stderr bytes.Buffer
	code := clicontract.Run(app, adapter, c.Argv, &stdout, &stderr)
	return Outcome{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

func envKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// matchClass checks one stream against its class.
func matchClass(stream, class, got string) error {
	switch {
	case class == "empty":
		if strings.TrimSpace(got) != "" {
			return fmt.Errorf("%s = %q, want empty", stream, truncate(got))
		}
	case class == "plain-text":
		if strings.TrimSpace(got) == "" || isOneJSONValue(got) {
			return fmt.Errorf("%s = %q, want plain text", stream, truncate(got))
		}
	case class == "json-value":
		if !isOneJSONValue(got) {
			return fmt.Errorf("%s = %q, want one JSON value", stream, truncate(got))
		}
	case class == "non-empty":
		if strings.TrimSpace(got) == "" {
			return fmt.Errorf("%s is empty, want non-empty", stream)
		}
	case strings.HasPrefix(class, "prefix:"):
		want := strings.TrimPrefix(class, "prefix:")
		if !strings.HasPrefix(got, want) {
			return fmt.Errorf("%s = %q, want the %q prefix", stream, truncate(got), want)
		}
	default:
		return fmt.Errorf("%s: unknown class %q", stream, class)
	}
	return nil
}

// isOneJSONValue reports whether s parses as exactly one JSON value.
func isOneJSONValue(s string) bool {
	dec := json.NewDecoder(strings.NewReader(s))
	var v any
	if err := dec.Decode(&v); err != nil {
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		return false
	}
	return true
}

// resolvePointer resolves an RFC 6901 pointer against a parsed document.
func resolvePointer(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	cur := doc
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			var i int
			if _, err := fmt.Sscanf(tok, "%d", &i); err != nil || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func truncate(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}
