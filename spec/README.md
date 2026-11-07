# cli-contract specification

The language-neutral definition of what a CLI built with
[`cli-contract`](../README.md) guarantees. The Go library is the reference
implementation; this directory is the contract it is tested against.

Version 1 is frozen against the behavior cli-contract v0.5.0 shipped, the
version avenor and paddock embed, plus the conventions later releases kept.
Where the written rule and the implementation disagree, the
[discrepancy ledger](v1-discrepancy-ledger.md) records the difference and its
disposition.

## The schema document

[`cli-contract.schema.json`](cli-contract.schema.json) is a JSON Schema
(draft-07) for the document a CLI prints as its machine-readable contract.
`App.SchemaDocument()` in the Go library renders one; any implementation can
render the same shape.

A document has two required members and a closed set of optional ones:

| Member | Meaning |
|---|---|
| `schema` | The document id. The library writes `cli-contract/cli-schema/v1`; an app that renders its own document keeps its own id. |
| `commands` | Top-level commands keyed by name, each with its flags, positionals, rules, output declaration, and subcommands. |
| `common_flags` | Flags registered on every command, keyed by name. |
| `name`, `version` | The binary name and the version `--version` reports. |
| `conditions` | The inputs a consumer supplies to evaluate availability rules. |
| `exit_codes`, `signals` | The app's named exit vocabulary and signal exit codes. |
| `handler_owned_signals` | The app installs its own signal handlers and chooses the exit code. |

Unknown members are rejected, so a typo cannot pass as a fact. Four members are
reserved for facts the existing consumers already emit: `docs`,
`typical_flow`, `cli_version`, and `envelope`.

### Two layers, two owners

The document keeps two kinds of statement apart, because their owners differ.

**Library-owned conventions.** Fixed for every conforming CLI:

- Help exits 0 with plain text, on the root, on every command, and through the
  `help` command. An unknown help topic is a caller error.
- `--version` exits 0 with the declared version line.
- Dispatch failures (unknown command, unknown or malformed flag, missing or
  extra positional) are usage errors: exit 2 and a diagnostic. Stdout carries
  the app's error output, which the app declares and a checker asserts as a
  class.
- SIGINT exits 130 and SIGTERM 143, unless the app declares
  `handler_owned_signals`.
- Single-dash long flags follow the documented rewrite rules and their
  exceptions.

**App-declared behavior.** Varies per CLI, so it is declared and then honored:

- `output.mode` per command: `json` (one JSON value), `raw` (bytes the
  consumer must not decode), or `none` (nothing).
- `output.success_exit_codes`: the exit codes that mean success. Required when
  a mode is declared, so two identical-looking declarations cannot publish
  different sets.
- `exit_codes`: the app's named vocabulary beyond 0/1/2. A declared code wins
  over the default split.

## What the schema cannot express

The JSON Schema checks the shape, not these semantics. A runner reads them
from this document:

- `use` and `url_positional` are derived from `positionals`. When they
  disagree, `positionals` is authoritative.
- A command that owns subcommands carries its **persistent** flags under
  `flags`, and declares no positionals of its own.
- Declaration order is not preserved: commands and flags are JSON objects.
- Help text is not pinned. Test the class (plain text, empty, one JSON value),
  never the wording.
- Stream framing is not described in v1. A stream command may declare `raw` or
  no output mode.

## Validating a document

Any JSON Schema draft-07 validator validates a document against
`cli-contract.schema.json`. In this repository, `go test ./...` also checks
that the document the library renders satisfies the published schema.
