package check

import (
	"testing"

	"github.com/sdougbrown/cli-contract"
)

// TestDeriveAssertsDeclaredErrorOutput pins the mode-governs-every-outcome
// rule: the unknown-flag case requires the stdout class the command declares.
func TestDeriveAssertsDeclaredErrorOutput(t *testing.T) {
	doc := &clicontract.SchemaDocument{
		Schema: "test/cli-schema/v1",
		Commands: map[string]clicontract.CommandSchema{
			"quiet":  {Short: "quiet", Use: "quiet", Output: &clicontract.Output{Mode: clicontract.OutputNone}},
			"result": {Short: "result", Use: "result", Output: &clicontract.Output{Mode: clicontract.OutputJSON}},
			"text":   {Short: "text", Use: "text", Output: &clicontract.Output{Mode: clicontract.OutputRaw}},
			"plain":  {Short: "plain", Use: "plain"},
		},
	}
	want := map[string]string{
		"unknown-flag:quiet":  ClassEmpty,
		"unknown-flag:result": ClassJSONValue,
		"unknown-flag:text":   ClassAny,
		"unknown-flag:plain":  ClassAny,
	}
	seen := map[string]bool{}
	for _, c := range Derive(doc) {
		class, ok := want[c.ID]
		if !ok {
			continue
		}
		seen[c.ID] = true
		if c.Want.Stdout != class {
			t.Errorf("%s stdout class = %q, want %q", c.ID, c.Want.Stdout, class)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("Derive produced no %s case", id)
		}
	}
}
