// Command fixturecli is the black-box checker's test target: a real
// binary that embeds one of the spec's fixture declarations, exposes it
// through a schema command, and runs the fixture handlers through the
// library. It exists to prove `cli-contract check` end to end; it is not
// a shipped command.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	clicontract "github.com/sdougbrown/cli-contract"
	"github.com/sdougbrown/cli-contract/conformance"
)

//go:embed declaration.json
var declaration []byte

func main() {
	if len(os.Args) > 1 && os.Args[1] == "schema" {
		os.Stdout.Write(append(declaration, '\n'))
		return
	}
	var decl conformance.Declaration
	if err := json.Unmarshal(declaration, &decl); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app, adapter := conformance.BuildApp(&decl)
	os.Exit(clicontract.Run(app, adapter, os.Args[1:], os.Stdout, os.Stderr))
}
