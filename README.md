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
}
```

Optional extensions:

- `TestSeams` — test-only crash (`BeforeWrite`) and signal injection
  (`PendingSignal`) points; production adapters skip it.
- `AfterWrite` — monitor-facing stderr summaries after the result is written.
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
schemas := app.Schemas()        // map[command name]CommandSchema
common := app.CommonFlagSchemas()
```

`CommandSchema` and `FlagSchema` render the declared surface as JSON-ready
data: names, Use strings, URL positionals, flag kinds (`string`/`bool`/
`repeat`), usage strings, defaults, and declared shorthands (`short`, omitted
when a flag has none). Compose the app's own contract facts
around them (statuses, error codes, envelope fields) into the document your
harnesses parse.

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
schema entry as `inputRules`, beside `flags`, and into the [registry](registry/registry.go)
manifest's tool block under the same key, for cli-registry to check before it
starts the process.

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
