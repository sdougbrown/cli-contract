# Spec v1 discrepancy ledger

This ledger freezes spec v1 against cli-contract v0.5.0 behavior as
inspected on 2026-11-02 (branch `feat/single-dash-long-flags`, PR #3).
The spec is normative. A row below records implementation drift or a
fixture gap; it does not introduce a new v1 behavior.

## Ledger

| ID | Normative decision | Go implementation | Shared fixture or action |
| --- | --- | --- | --- |
| V1-01 | The declaration document is one object: `version`, `name`, `appVersion`, `behavior`, `commonFlags`, `commands` (spec §4). | Drift: the library renders the pieces (`App.Schemas()`, `App.CommonFlagSchemas()`); no function composes the full document, so composition is app-side today. | Spec stage 2 adds a `Document()` renderer and the library runner builds fixture CLIs from whole documents. |
| V1-02 | Every command declares `output` (`json-value` / `raw-text` / `none`, spec §3.1). | Drift: `CommandSchema` has no output field; the dispatch behavior exists (Handle writes one value, Raw writes directly) but is not declared data. | Fixture declarations carry `output`; the emitter gains the field in stage 2. |
| V1-03 | The app declares its exit-code vocabulary in `behavior.exitCodes`, and a handler-decided code the vocabulary names wins over the usage/operational split (spec §3.2). | Drift: exit codes beyond 0/1/2 are adapter facts (`Adapter.ExitCode`); nothing declares or validates them. | Fixture cases whose expected exit code is outside 0/1/2 carry the declared code; the checker validates declared codes only. |
| V1-04 | Positionals are declared as a list of `{name, required, variadic}` (spec §4). | Drift: `CommandSchema` renders only the lossy `url_positional` convenience; the declaration list exists on `Command.Positionals` but is not emitted. | Fixture declarations carry full positionals; the emitter replaces `url_positional` with the list in stage 2. |
| V1-05 | Each flag may declare a single-letter `short`, with `h` reserved for help unless the flag is named `help` (spec §4, §2.5). | Drift: `FlagSchema` does not emit `short`; the reserved-shorthand validation exists (`TestValidateRejectsReservedShorthandOnPersistentFlags`, `TestDeclaredHelpFlag`). | Fixture declarations carry `short` where the case needs it; the emitter gains the field in stage 2. |
| V1-06 | `behavior.bareHelp` and `behavior.handlerOwnedSignals` are declared facts (spec §3.3, §2.4). | Partial: the behaviors exist (`App.BareHelp`, `App.HandlerOwnedSignals`); the schema document does not carry them. | Fixture declarations carry both; emitter alignment in stage 2. |
| V1-07 | A command that owns subcommands declares no flags of its own; the parent's `flags` are the persistent flags its subcommands inherit (spec §4). | Aligned: `Command.Schema()` swaps the parent's entry to `PersistentFlags` and drops its flags. | `persistent-flag` fixture cases cover inherited parsing. |
| V1-08 | The version line is `<name> <version>` followed by a newline (spec §2.2). | Partial: the default template matches, but `App.VersionTemplate` may render anything. | The spec pins the default; a fixture exercises the default form only. Custom templates are an app-side deviation, not a v1 behavior. |
| V1-09 | Single-dash long-flag rewrite rules and exceptions (spec §2.5). | Aligned as of v0.5.0 (PR #3): the rewrite walks the declared tree; shorthand registration covers every kind. | The single-dash fixture group pins every rule and exception. |
| V1-10 | Signal cases run only against a declaration that carries a long-running command (spec §5). | Aligned in intent: the library convention is testable through the test seams (`TestRunSignals`); no portable long-running command exists yet. | Stage 2 adds a fixture `sleep` command; the black-box checker skips these cases without one. |
| V1-WIN-01 | Platform scope. | Spec v1 is POSIX-only: signal exit codes (130/143) and signal cases assume POSIX signals. Windows conformance (exit-code and signal semantics, console Ctrl-C) is out of scope for v1 and deferred until a Windows test environment exists. | A Windows compliance pass opens its own ledger rows when it starts. |

## Fixture conventions

Stage 2 (the conformance fixtures PR) owns the fixture index and the
first fixture group. The fixture IDs are stable once merged;
implementations consume the fixture index rather than copying examples
into implementation-specific tests.

## Disposition

Every row has an owner action in stage 2 or later. No row is an
unresolved normative decision. This PR changes only the spec: no library
behavior changes here.
