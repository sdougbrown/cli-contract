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

## Compatibility in v0.5.0

These declarations and invocations are the exceptions the release
introduces:

- `Short` registers for every flag kind. Before, only `Bool` registered its
  shorthand and the other kinds dropped it silently, so `Short: "n"` on an
  `Int` flag now means `-n`.
- Declarations that were silently accepted now fail validation at startup:
  a multi-letter shorthand, two flags sharing a shorthand within one merged
  flag set, and `Short: "h"` on a command whose merged flag set declares no
  flag named `help`.
- A non-bool flag declared `Short: "v"` on an app with a `Version` now takes
  `-v` itself; before, the shorthand was ignored and `-v` printed the
  version.
- A nested command declared `NoInterspersed` now stops flag parsing at its
  first positional, as a top-level one already did, so dashed tokens after
  that positional stay positionals.

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

## Schema

```go
schemas := app.Schemas()        // map[command name]CommandSchema
common := app.CommonFlagSchemas()
```

`CommandSchema` and `FlagSchema` render the declared surface as JSON-ready
data: names, Use strings, URL positionals, flag kinds (`string`/`bool`/
`repeat`), usage strings, and defaults. Compose the app's own contract facts
around them (statuses, error codes, envelope fields) into the document your
harnesses parse.

## Parse without running

```go
inv, err := clicontract.Parse(app, argv)
```

Same tree, same validation, no handlers: for harnesses that validate argv
before executing, and for tests that assert on parsed invocations.

## License

Apache 2.0
