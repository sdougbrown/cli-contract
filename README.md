# cli-contract

Declare a CLI's command surface once. Derive everything downstream consumers
read from it: the cobra command tree, the machine-readable schema document,
and the run orchestration that routes caller errors and exit codes.

The library is the producer half of contract-as-data. Its companion,
[cli-registry](https://github.com/sdougbrown/cli-registry), is the consumer
half: manifests describing CLIs as typed tool calls. A CLI that embeds
`clicontract` emits the machine-readable surface its manifests can be
generated from.

## Declare

```go
app := &clicontract.App{
    Name:    "mycli",
    Version: version,
    RootShort: "mycli does one thing",
    Commands: []*clicontract.Command{
        {
            Name:  "fetch",
            Short: "fetch a resource",
            Positionals: []clicontract.Positional{{Name: "URL", Required: true}},
            Flags: []clicontract.Flag{
                {Name: "depth", Kind: clicontract.String, Usage: "fetch depth (default 3)", Default: "3"},
            },
            Handle: func(ctx context.Context, inv clicontract.Invocation, stderr io.Writer) (any, error) {
                return map[string]any{"status": "ok"}, nil
            },
        },
        {
            Name:  "store",
            Short: "store utilities",
            Subcommands: []*clicontract.Command{
                {Name: "path", Short: "print the store path", Handle: handlePath},
                {Name: "reset", Short: "delete the store", Handle: handleReset},
            },
        },
    },
    CommonFlags: []clicontract.Flag{
        {Name: "json", Kind: clicontract.Bool, Usage: "no-op; output is always JSON"},
    },
}
```

## Run

```go
os.Exit(clicontract.Run(app, adapter, os.Args[1:], os.Stdout, os.Stderr))
```

`Run` builds the tree (help, `--version`, flag parsing, positional rules),
dispatches, and routes failures. The app supplies its result vocabulary
through an [`Adapter`](#adapter): the library never formats envelopes or
chooses app exit codes.

The conventions the library owns:

- Help exits 0 with plain text — on the root, on every command, and through
  the `help` command; an unknown help topic is a caller error.
- `--version` exits 0 with `<name> <version>`.
- Terminating signals win over everything: SIGINT exits 130, SIGTERM 143.
- Every dispatch-level failure (unknown command, bad flag, bad positional)
  arrives at the adapter as a `*UsageError` and is the app's to format.

## Single-dash long flags

`Run` and `Parse` hand cobra the argv untouched. When parsing fails with
an error a single-dash long flag produces — pflag's unknown-shorthand and
unknown-flag errors on the command cobra resolved, or an unresolved command
name — the library rewrites the single-dash spellings of that command's
long flags to their double-dash form (`-agent x` becomes `--agent x`) and
parses again. Only the failing command's own segment is rewritten: the
flags before the positional where cobra descends for a command that owns
subcommands, and everything left for a leaf. A rewrite can expose a deeper
command, so the passes repeat while one rewrites a token. An argv that
already parsed is never touched, and every failure before dispatch reports
the original error.

The precedence a single dash carries:

- A single-dash token that parses as declared shorthands keeps that
  meaning: with `-v` and `-b` declared as bool shorthands, `-vb x` still
  sets both bools and `-vb=false` still sets v and clears b. The token
  becomes a long flag only when the shorthand reading fails — a lone long
  flag `-agent x`, or a cluster that hits an undeclared letter, rewrites.
- A token after a value-taking flag is that flag's value, single dash or
  not: `--label -format probe tgt` still sets label to `-format`.
- Tokens after a bare `--` are positionals and are never rewritten, and a
  command declared `NoInterspersed` — top-level or nested — leaves
  everything after its first positional alone.
- Single-dash common flags work before the subcommand name
  (`-format json probe tgt`); leaf-only flags typed before their
  subcommand name still fail, as they did before.

`Invocation.Supplied` records the flags given on the command line, every
kind included, with inherited persistent and common flags landing on the
leaf's invocation. A defaulted flag that was not typed keeps its default
value and stays out of the map, so an explicitly-passed zero is
distinguishable from absent.

## Compatibility in v0.5.0

An argv that parsed under v0.4.x takes the same first pass untouched. These
declarations and invocations are the exceptions the release introduces:

- `Short` registers for every flag kind. Before, only `Bool` registered its
  shorthand and the other kinds dropped it silently, so `Short: "n"` on an
  `Int` flag now means `-n`.
- Declarations that were silently accepted now fail validation at startup:
  a multi-letter shorthand, two flags sharing a shorthand within one merged
  flag set, and `Short: "h"` on a command whose merged flag set declares no
  flag named `help`. A flag that shadows an inherited flag of the same name
  with a different kind is rejected too; before, it failed on every
  invocation instead of at startup.
- A non-bool flag declared `Short: "v"` on an app with a `Version` now takes
  `-v` itself; before, the shorthand was ignored and `-v` printed the
  version.
- A nested command declared `NoInterspersed` now stops flag parsing at its
  first positional, as a top-level one already did, so dashed tokens after
  that positional stay positionals.
- With nil argv the library reads `os.Args[1:]` itself, so the error
  envelope's `command` and `Invocation.Command` report `os.Args[1]`
  instead of an empty string.

## Adapter

```go
type Adapter interface {
    ErrorResult(command string, err error) any
    Write(w io.Writer, result any) error
    ExitCode(inv Invocation, result any, handlerErr error) int
    Deadline(ctx context.Context, inv Invocation) (context.Context, context.CancelFunc)
    BeforeHandler(inv Invocation, stderr io.Writer)
    Validate(inv Invocation) error
}
```

### Streaming output

Use `Command.Stream` for output produced during an invocation. A stream
handler receives stdout and stderr and returns an app-defined result and error:

```go
Stream: func(ctx context.Context, inv clicontract.Invocation, stdout, stderr io.Writer) (any, error) {
    // Write records to stdout while honoring ctx cancellation.
    return result, err
},
```

The result is passed to `Adapter.ExitCode`, not written to stdout. On an error,
the adapter's `ErrorResult` supplies the result for exit classification and
`HandlerDiagnostics` controls the stderr diagnostic. The stream owns its
framing, including any terminal error record. An output write failure exits
with the operational code, even if the handler ignores the error; when the
handler also returns an error, that error supplies the result and diagnostic
and the write failure decides only the exit code. Partial output cannot be
retracted.

Streams use the same validation, `BeforeHandler`, deadline, `AfterWrite`, and
signal handling as `Handle`; as there, a failed output write skips
`AfterWrite`. Before the stream starts, parsing and validation errors use the
ordinary adapter-written usage response. Once it starts, the library never
appends an envelope. A signal can leave previously written output in place;
handlers must return when their context is canceled. The adapter decides how
to classify deadline and cancellation errors.

`Handle`, `Raw`, and `Stream` are exclusive on a leaf command. Schema and
registry output-mode declarations do not yet describe streams; see #2.

Optional extensions:

- `TestSeams` — test-only crash (`BeforeWrite`) and signal injection
  (`PendingSignal`) points; production adapters skip it.
- `AfterWrite` — monitor-facing stderr summaries after a result or stream
  writes its output; a failed write skips it, and a raw command never calls it.
- `HandlerDiagnostics` — format or silence the stderr diagnostic for
  handler-owned failures: a dispatched command's error, a Validate
  rejection, or a raw-output command's failure (`HandlerDiagnostic(inv,
  err) string`; an empty string prints nothing).
- `DispatchDiagnostics` — format or silence the stderr diagnostic for
  failures before dispatch, unknown command, bad flag, or bad positional
  (`DispatchDiagnostic(command, err) string`; an empty string prints
  nothing). It takes the command path the failure resolved to ("store
  reset"; the app name when nothing resolved) rather than an Invocation,
  since no Invocation exists before dispatch.

## Schema

```go
doc, err := app.SchemaDocument() // the whole document, ready to encode
if err != nil {
	return err // a malformed declaration fails here, not at encode time
}
schemas := app.Schemas()        // map[command name]CommandSchema
common := app.CommonFlagSchemas()
```

`SchemaDocument` renders the declared surface as one document: the command
entries, the common flags, the availability conditions, and the signal
convention the library owns. The shape is specified in [spec/](spec/).
`CommandSchema` and `FlagSchema` carry names, help text, structured
positionals, Use strings, URL positionals, single-dash behavior, flag kinds
(`string`/`bool`/`repeat`/`integer`/`duration`), usage strings, defaults,
declared shorthands (`short`, omitted when a flag has none), input and
availability rules, and the output declaration. Compose the app's own facts
(statuses, error codes, envelope fields, named exit codes) around it before
encoding.

### Output declaration

`Command.Output` states how one leaf command's stdout is decoded and which
exit codes mean success:

```go
Output: clicontract.Output{
    Mode:             clicontract.OutputJSON, // json | raw | none
    SuccessExitCodes: []int{0, 3},
},
```

The [registry](registry/registry.go) bridge copies the declaration into a
manifest's `output` block instead of guessing it. It translates a JSON pointer
to the manifest's `json-pointer` mode and `none` to `raw`, since a command
that writes nothing has nothing to decode. A declared mode requires
`SuccessExitCodes`, so a declared block publishes exactly the codes its author
stated and the envelope default stays unique to commands that declare none.

The v1 spec's [conformance fixtures](spec/) run against the library as part of
`go test ./...`, so the written contract and the implementation are checked
together.

## Check a CLI

```sh
go build -o cli-contract ./cmd/cli-contract
./cli-contract check ./mycli                        # runs `mycli schema`
./cli-contract check --schema-file doc.json ./mycli  # for a CLI with no schema command
```

The checker reads the schema document, derives the cases the declared surface
implies — help on every command, an unknown flag on every command, an unknown
command, a missing required positional, the version line — and runs them
against the binary as a black box. A CLI that does not use this library
conforms if it passes.

The report is one JSON value with one entry per case. Exit 0 means every case
passed, 1 means at least one failed, and 2 means the check could not run. A
check that could not run says whose failure stopped it: an unreadable
`--schema-file` reports `error.code: "usage"`, while a target whose schema
command fails, hangs, or prints an unreadable document reports a non-usage
class. Signal cases are not derived: they need a command that stays alive,
which the surface does not declare. The scheduled
[consumer workflow](.github/workflows/conformance.yml) runs the checker against
pinned releases of the CLIs that use the library.

## Availability

Declare, per command, the Umpire rules that decide whether a consumer offers
its tool, and the inputs those rules read:

```go
app := &clicontract.App{
    AvailabilityConditions: []clicontract.Condition{
        {Name: "read_only", Type: clicontract.ConditionBoolean, Description: "the profile is read-only"},
    },
    Commands: []*clicontract.Command{
        {
            Name:  "edit",
            Short: "edit a thing",
            Handle: handleEdit,
            Availability: []*clicontract.Rule{
                // Rules without an explicit field gate their own tool;
                // targets in a disables rule are command paths.
                clicontract.Enabled(clicontract.Not(clicontract.When("read_only")), "the profile is read-only"),
            },
        },
    },
}
```

Rule vocabulary: `Enabled`/`EnabledOn` (enabledWhen), `Requires`/`RequiresOn`,
`Disables`, `AnyOf`, `EitherOf`; expressions via `When`, `CondEq`, `CondIn`,
`And`, `Or`, `Not`. The declarations validate at startup alongside the rest
of the surface, and the [registry](registry/registry.go) package renders them
into a cli-registry manifest's top-level availability block, which the
consumer evaluates with [`umpire-go`](https://github.com/umpire-tools/umpire-go).

## Input rules

Each declared flag is independent of its siblings, so a document generated from
a declaration accepts invocations the CLI rejects at run time. `InputRules`
names those relationships once, over the flags the command parses:

```go
InputRules: []clicontract.Rule{
    // A roster file only makes sense with an entry name.
    clicontract.RequiresOn("roster-file", clicontract.Present("roster-entry"), "a roster file needs an entry"),
    // The two prompt sources are alternatives.
    clicontract.RequiresOn("prompt-file", clicontract.Absent("prompt"), "cannot combine with --prompt"),
},
```

The vocabulary is availability's, in the field dialect: `RequiresOn`/`Requires`,
`EnabledOn`/`Enabled`, `Disables`, `AnyOf`, and `EitherOf` over `Present`, `Absent`,
`Truthy`, `Falsy`, `Eq`, `In`, `NotIn`, `And`, `Or`, and `Not`. A rule names the
flag it constrains in its field, and a `disables` target is a flag name. The
dialects do not mix: availability rules compare conditions a consumer supplies
at load, input rules compare the command's own inputs, and a declaration that
crosses them fails at startup.

Every rule above is one-directional: a supplied `--roster-file` needs
`--roster-entry`, while an entry can stand alone. A pair that must be given
together gates each flag on the other instead. Two `requires` rules pointing at
each other form a dependency cycle, which fails at startup with the loop and the
constructor to use:

```go
InputRules: []clicontract.Rule{
    clicontract.EnabledOn("roster-file", clicontract.Present("roster-entry"), "a roster file needs an entry"),
    clicontract.EnabledOn("roster-entry", clicontract.Present("roster-file"), "an entry needs a roster file"),
},
```

A flag declared `Required` is checked at the same point, which moves that
failure from the handler to the parser.

The rules validate with the rest of the surface and render into the command's
schema entry as `input_rules`, beside `flags`, and into the [registry](registry/registry.go)
manifest's tool block as `inputRules`, the key cli-registry already reads,
before it starts the process.

`Run` and `Parse` enforce them too, so a handler never re-checks a relationship
the declaration already states: a violating invocation is a caller error (exit
2) carrying the rule's reason.

## Parse without running

```go
inv, err := clicontract.Parse(app, argv)
```

Same tree, same validation, no handlers: for harnesses that validate argv
before executing, and for tests that assert on parsed invocations. Nil
argv is the process's own arguments, as in `Run`.

## License

Apache 2.0
