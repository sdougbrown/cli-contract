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
  consumer must not decode), or `none` (nothing). The mode governs **every**
  outcome, not only success: a `json` command writes one JSON value on a
  dispatch failure too (the error envelope), a `raw` command writes text or
  bytes, and a `none` command writes nothing. Exit 2 and the stderr diagnostic
  are unchanged.
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

### Input rules and the invocation

A command's `input_rules` are Umpire rules in the field dialect over the
command's flag names. A rule with a `field` names the flag it constrains, and
it is a violation only when that flag is supplied. A `requires` or
`enabledWhen` rule with **no** `field` constrains the invocation as a whole:
its `when` expression must hold on every invocation, including one that
supplies no flags. A `disables` rule acts through its `targets` in both slots
and carries no field.

Evaluate a fieldless gate rule's `when` over the supplied flag values with
Umpire's field-dialect semantics. An absent flag and an explicitly empty string
are both falsy for `truthy`, so `--flag=` does not satisfy a presence-or-value
requirement. A failing rule rejects the invocation with the rule's `reason`.

Umpire's `requires` and `enabledWhen` rules must name a field, so a consumer
that feeds `input_rules` to an Umpire engine compiles a fieldless gate rule
onto a synthetic field reserved for the whole invocation, leaving `anyOf` and
`eitherOf` wrappers without a field as rendered.

## Validating a document

Any JSON Schema draft-07 validator validates a document against
`cli-contract.schema.json`. In this repository, `go test ./...` also checks
that the document the library renders satisfies the published schema.

## Conformance fixtures

[`index.json`](index.json) lists the fixtures. Each fixture is one declaration
plus the cases that declaration must pass, in the same shape as
`umpire-spec/conformance`:

```json
{
  "fixtureVersion": 1,
  "id": "help-and-version",
  "description": "Short human summary",
  "document": { "schema": "cli-contract/cli-schema/v1", "commands": {}, "common_flags": {} },
  "cases": [
    {
      "id": "version",
      "argv": ["--version"],
      "expect": {
        "exit": 0,
        "stdout": { "class": "plain-text", "equals": "fixture 1.0.0\n" },
        "stderr": { "class": "empty" }
      }
    }
  ]
}
```

- `document` is a schema document. A runner validates it against
  `cli-contract.schema.json` before it runs anything.
- `signal` is optional: `SIGINT` or `SIGTERM`. The in-process runner injects
  it through the library's test seam.
- `stdout` and `stderr` may be omitted. A signal case that asserts only the
  exit code omits both.

### Stream classes

| Class | Means |
|---|---|
| `empty` | Nothing after trimming whitespace. |
| `plain-text` | Non-empty, and not one JSON value. `equals` pins the exact text. |
| `json-value` | Exactly one JSON value. |
| `json-matches` | The JSON value has a value at `pointer` (RFC 6901) equal to `equals`. |
| `non-empty` | stderr: something was written. |
| `prefix` | stderr: starts with `text`. |

Cases assert a class, never help wording. That is what keeps a port from
being pinned to cobra's text.

### The fixture app

A runner builds the app the document declares and supplies one handler per
leaf command path. This repository's reference runner echoes the parsed
invocation for a result command (`{"command", "path", "args", "flags",
"bools", "ints", "supplied"}`), returns text for a `raw` command, and returns
nothing for a `none` command. Its adapter writes stdout as the document's
`output.mode` declares, on every outcome: a dispatch failure on a `none`
command writes nothing, on a `raw` command writes text, and on a `json`
command writes the error envelope. A port may choose its own handlers, but it
must honor the same declaration, applying the mode declared for the command
path the failure resolved to.

### Writing a port runner

1. Read `index.json`, resolving each `path` against the directory that
   contains `index.json`.
2. Parse the fixture and validate `document` against the schema.
3. Build the app the document describes, mapping each leaf path to a handler.
4. For each case, run `argv` and compare the exit code and the stream classes.
5. Fail on wording. Pass on class.

Signal cases need an in-process seam or a target that stays alive long enough
to be signalled. The black-box checker has no signal support in v1: the
surface does not declare a command that stays alive. Windows is out of scope
for v1 signal and exit-code cases.

