// Command fixture is a minimal CLI target for the checker's end-to-end tests.
// It serves its schema document under a non-default command name, so a test
// can prove the checker forwards --schema-command instead of hardcoding the
// default, and it answers the surface the derived cases exercise.
package main

import (
	"fmt"
	"os"
	"strings"
)

const schemaDocument = `{
	  "schema": "cli-contract/cli-schema/v1",
	  "name": "fixture",
	  "commands": {
	    "only": {"short": "do one thing", "use": "only", "url_positional": false}
	  },
	  "common_flags": {}
	}`

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "dump-schema" {
		fmt.Println(schemaDocument)
		return
	}
	if len(args) > 0 && args[len(args)-1] == "--help" {
		fmt.Printf("usage: fixture %s\n", strings.Join(args, " "))
		return
	}
	fmt.Fprintf(os.Stderr, "fixture: unsupported invocation %q\n", strings.Join(args, " "))
	os.Exit(2)
}
