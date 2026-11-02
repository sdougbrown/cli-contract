# The CLI behavior contract — specification v1

Status: frozen against cli-contract v0.5.0 behavior. Implementation drift
is recorded in the [v1 discrepancy ledger](v1-discrepancy-ledger.md); a
ledger row never introduces new v1 behavior.

The contract has two layers with different owners:

- **Library-owned conventions** (section 2): fixed for every conforming
  CLI. Help, version, dispatch failures, signal exits, and the
  single-dash long-flag rules.
- **App-declared behavior** (section 3): varies per CLI, but a conforming
  CLI declares it in its document and then honors the declaration. Output
  mode per command, the exit-code vocabulary beyond the conventions, and
  signal-handling ownership.

The key words MUST, MUST NOT, SHOULD, and MAY are normative.

## 1. Vocabulary

- **Declaration document** — the JSON object a CLI emits that describes
  its surface: name, app version, common flags, commands, and the
  app-declared behavior. Section 4 defines it;
  [`cli-contract.schema.json`](cli-contract.schema.json) validates it.
- **Case** — one invocation a conforming CLI must satisfy: an `argv`
  (without the program name), optional environment additions, an optional
  signal, and the expected exit code plus stdout and stderr classes.
- **Class** — the observable shape of one stream: `empty`, `plain-text`,
  `json-value`, or `json-matches:<pointer>` for stdout; `empty`,
  `non-empty`, or `prefix:<text>` for stderr. Classes, never exact help
  wording, so implementations are not pinned to one help renderer.

## 2. Library-owned conventions

A conforming CLI MUST follow every rule in this section.

### 2.1 Help

- A root invocation with `--help` or `-h` MUST exit 0 and MUST write
  plain text help to stdout. Nothing else (no JSON envelope) MAY appear
  on stdout for a help path.
- Every declared command MUST support a help path — `<command> --help`,
  `-h`, and the `help` command (`mycli help <command>`) — with the same
  exit and stream rules. Nested commands support it at every level.
- `help <unknown>` is a caller error: it MUST follow the dispatch-failure
  rules (2.3).
- A bare invocation with no arguments MUST fail as a caller error, unless
  the app declares `bareHelp`, in which case it MUST print the root help
  and exit 0.

### 2.2 Version

- `--version` on the root MUST exit 0 and MUST write one version line to
  stdout in the form `<name> <version>` followed by a newline, where
  `<name>` and `<version>` are the declared app name and version. A
  single-dash `-version` form follows the single-dash rules (2.5) and
  MUST behave identically.
- `--version` on a subcommand resolves to the root and MUST print the
  version line, exit 0.

### 2.3 Dispatch failures

- An unknown command, an unknown or malformed flag, a missing required
  positional or positional excess, and an unknown `help` topic are caller
  errors. A caller error MUST exit 2 and MUST write a diagnostic to
  stderr.
- On stdout, a caller error writes nothing beyond the app's error
  envelope: an envelope-style app (output mode `json-value`) writes
  exactly one usage-classified error envelope (stdout class
  `json-value`); a non-envelope app writes nothing.
- Exit code 2 is reserved for caller errors. Exit code 1 is reserved for
  operational failures. Exit code 0 means success.

### 2.4 Signals

- Unless the app declares `handlerOwnedSignals`, a terminating SIGINT
  MUST exit 130 and a terminating SIGTERM MUST exit 143, regardless of
  how far the invocation progressed. The CLI MUST NOT write a result to
  stdout after the signal arrives; a partially written result MAY remain.
- When the app declares `handlerOwnedSignals`, the library (or the
  hand-written equivalent) stays out of the way: the handler installs its
  own signal wiring and chooses the exit code, which MUST be one the
  declared vocabulary names (3.2).

### 2.5 Single-dash long flags

A conforming CLI accepts the standard flag package's single-dash
long-flag forms for every declared long flag, inherited flags included.

- `-name`, `-name=value`, and `-name value` MUST parse as the long flag
  `name`. A single-dash token whose name is longer than one character and
  spells a declared long flag MUST be read as that flag.
- A single-dash token that is not a declared long name passes through:
  shorthand clusters (`-vf`) parse as shorthands, an undeclared name
  (`-bogus`) is a caller error, and a token that follows a value-taking
  flag is that flag's value.
- A long name beats a shorthand cluster of the same letters: with bool
  `-v` and a long `vb` declared, `-vb x` sets `vb` to `x`.
- A value-taking shorthand absorbs the next token, lone (`-l x`) or at
  the end of a cluster (`-vl x`); a flag-looking token after it stays the
  shorthand's value.
- Tokens after a bare `--` are never rewritten.
- A command that declares non-interspersed parsing keeps every token
  after its first positional as a positional, flags included.
- `-version` on the root, and `-version <command>`, follow 2.2.

## 3. App-declared behavior

A conforming CLI declares the following in its document and MUST honor
the declaration. Absence of a field takes the default named here.

### 3.1 Output mode

Every leaf command declares one output mode:

- `json-value` (default) — the command writes exactly one JSON value to
  stdout and nothing else on success.
- `raw-text` — the command writes text to stdout that is not a JSON
  value.
- `none` — the command writes nothing to stdout on success.

### 3.2 Exit-code vocabulary

- The conventions own 0 (success), 1 (operational failure), 2 (caller
  error), and the signal codes 130/143.
- An app MAY declare additional codes in `behavior.exitCodes`, mapping
  each code to a description of the condition that produces it.
- Precedence: when a handler decides an exit code, the code the declared
  vocabulary names wins over the usage/operational split. A handler MUST
  NOT return a code outside the conventions and the declared vocabulary;
  a checker treats an undeclared nonzero exit as a violation.

### 3.3 Handler-owned signals

`behavior.handlerOwnedSignals` (default false) declares that the app's
handlers own signal handling. When true, the conventions' signal exits
(2.4) do not apply, and every exit a signal path can produce MUST be in
the declared vocabulary.

## 4. The declaration document

The document is a JSON object with `version` (the constant 1), `name`,
`appVersion`, optional `errorPrefix`, `behavior` (3.1–3.3),
`commonFlags`, and `commands`. Each command carries its `short` help, an
optional `use` string, `positionals`, `output` (3.1), `flags`, and
`subcommands`; a command that owns subcommands declares no flags of its
own — flags inherited by its subcommands are declared in the parent's
`flags` as persistent flags. Flag kinds are `string`, `bool`, `repeat`,
`integer`, and `duration`; each flag carries `usage`, an optional
`default` (the value an absent flag yields), `required`, and an optional
single-letter `short`.

[`cli-contract.schema.json`](cli-contract.schema.json) is the normative
machine-readable form of this section.

## 5. Conformance fixtures

The `conformance/` directory holds plain JSON fixtures: an `index.json`,
one file per fixture, each with a declaration document plus cases. Every
implementation — the reference Go library first — reads the same index so
the spec and the implementations cannot drift. The fixture shape and the
runner contract are documented in `conformance/README.md`.

Signal cases need a command that stays alive long enough to be signalled:
a fixture's declaration that carries signal cases MUST declare a
long-running command, and the black-box checker skips signal cases when
the target does not declare one.

## 6. The black-box checker

`cli-contract check <binary>` asks the target for its declaration
document — the target exposes it through a `schema` command that writes
the document to stdout — derives cases from the declared surface: help on
every command, an unknown flag on every command, a missing required
positional, `--version` — and runs them, reporting every violation of
section 2. It works on CLIs that do not embed the library. Exit-code
vocabulary checks (3.2) run against the declared codes; signal cases run
only when the target declares a long-running command (5).
