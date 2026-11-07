# cli-contract v1 discrepancy ledger

This ledger freezes the v1 contract before conformance fixtures are written
against it. It compares the normative decisions in
[`README.md`](README.md) with cli-contract v0.5.0, the version avenor and
paddock embed, and with the current library (v0.9.0), which git-feedback uses.

The contract is normative. A row records implementation drift, an app-declared
extension, or a deliberate gap; it never introduces new v1 behavior.

| ID | Written rule | Implementation as found | Disposition |
| --- | --- | --- | --- |
| CC-01 | Dispatch failures are usage errors and exit 2. | The library classifies them as `*UsageError` and returns `Adapter.ExitCode(...)`; the exit code is the adapter's (`run.go`). Every consumer adapter maps the usage class to 2. | Conforming: v1 fixes the observed behavior and asserts exit 2 with a diagnostic, so an adapter that picks another code does not conform. |
| CC-02 | The exit-code vocabulary is 0 ok, 1 operational, 2 usage; an app may declare more. | avenor's adapter lets a handler-decided code win over the split, mapping run stop reasons to 124 and 130. | App-declared, not drift: the code is conforming when `output.success_exit_codes` and `exit_codes` declare it. The document carries the declaration; the checker reads it instead of assuming 0/1/2. |
| CC-03 | Help exits 0 with plain text; an unknown help topic is a caller error. | `App.BareHelp` chooses between help-and-0 and missing-subcommand-and-2 for a bare invocation. The document does not carry the choice. | Gap, out of v1: a bare invocation is not a conformance case. A runner may exercise it, but the checker cannot predict it from the document. |
| CC-04 | Help output is plain text. | Cobra's help wording differs by version and by app; the library passes `Short`/`Long` through unchanged. | Conforming: fixtures assert the stdout class and exit code, never the text. |
| CC-05 | `output.mode` states how stdout is decoded. | Nothing described a stream command's framing: `Command.Stream` writes incrementally and owns its own records. | Gap, out of v1: a stream command may declare `raw` or no output mode. Describing stream framing is future work. |
| CC-06 | SIGINT exits 130 and SIGTERM 143 unless the app owns signals. | The library watches both signals and re-checks after output; a handler-owned app installs its own handlers. | Conforming: the library runner injects the signal in-process and asserts the code. The black-box checker has no signal support in v1, because the surface does not declare a command that stays alive. Windows is out of scope for v1 signal and exit-code cases. |
| CC-07 | A flag default is the value the parser applies: a number for `integer`, a Go duration string for `duration`. | Before this release the document rendered an integer default as `""` and a duration as its nanosecond count. | Fixed in v1: `defaultOf` renders each kind's applied value, and a round-trip test proves the document rebuilds the parser it describes. |
| CC-08 | A command that owns subcommands declares no positionals and carries its persistent flags under `flags`. | The renderer nulls `flags`, `positionals`, and `url_positional` for a parent, then writes `PersistentFlags` under `flags`; the builder reads the same convention back. | Conforming: the rule is one rendering path in both directions. The JSON Schema cannot express it, so [`README.md`](README.md) states it as a semantic. |

| CC-09 | An early draft of this contract said a dispatch failure writes nothing on stdout. | The library hands the error to the adapter, which writes its error result: `git-feedback nope` prints a JSON envelope on stdout, a diagnostic on stderr, and exits 2. | Conforming: v1 fixes exit 2 and a diagnostic; stdout is the app's error output, asserted as a class rather than as empty. |

## Fixture conventions

Fixture ids are stable. A runtime implementation consumes the fixture index
rather than copying cases into implementation-specific tests. A case asserts
an argv, optional signal, exit code, and stream classes; it never asserts help
wording or envelope prose.

## v1 disposition

Every row has an owner and a disposition. No row is an unresolved normative
decision. v1 does not change any consumer's behavior: it freezes what the
library and its consumers already do, and records the three deliberate gaps
(CC-02, CC-03, CC-05) where the document carries less than a runner can
observe.
