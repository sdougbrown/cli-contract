# cli-contract conformance fixtures

These fixtures are the cross-implementation conformance target for the
[CLI behavior contract](../spec.md) v1. They are plain JSON so a Rust,
Python, or TypeScript port — and the reference Go library — consume the
same files without translating test code first.

## Fixture shape

```json
{
  "fixtureVersion": 1,
  "id": "conventions",
  "description": "Short human summary",
  "declaration": { "version": 1, "name": "probecli", "commands": {} },
  "cases": [
    {
      "id": "root-help-long",
      "argv": ["--help"],
      "env": { "FIXTURE_MARKER": "hello" },
      "signal": "SIGINT",
      "expectedExitCode": 0,
      "stdout": "plain-text",
      "stdoutMatches": { "/status": "ok" },
      "stderr": "empty"
    }
  ]
}
```

- `declaration` is a normal v1 declaration document
  ([spec.md](../spec.md) §4; [cli-contract.schema.json](../cli-contract.schema.json)
  validates it).
- `argv` is the invocation without the program name.
- `env` adds or overrides environment variables for the case; omitted
  means the runner's environment unchanged.
- `signal` is optional (`SIGINT` or `SIGTERM`); the runner sends it once
  the process is alive. Only declarations that carry a long-running
  command (the `sleep` command) have signal cases.
- `expectedExitCode`, `stdout`, and `stderr` are the expected exit code
  and the stdout and stderr classes.

Classes:

- stdout: `empty`, `plain-text`, `json-value`, or
  `json-matches:<pointer>` (the parsed stdout contains the RFC 6901
  pointer).
- stderr: `empty`, `non-empty`, or `prefix:<text>` (the first stderr
  line starts with the text).
- `stdoutMatches` (optional) maps RFC 6901 pointers to expected JSON
  values, evaluated against the parsed stdout; it pins values without
  pinning the whole envelope.

All paths in `index.json` are relative to `index.json` itself (the
`conformance/` directory). Resolve them against the directory containing
`index.json`.

## The handler contract

A runner builds a fixture CLI from each fixture's declaration. The
handlers are the same in every implementation, so the expected values in
`stdoutMatches` pin portable behavior:

- A leaf command with output mode `json-value` returns one JSON object:
  `{"command": "<command path, space-joined>", "status": "ok"}` plus
  every parsed flag echoed under its name (strings as strings, bools as
  booleans, integers as numbers, durations as their duration strings)
  and the positional values under `args` as an array of strings.
- The `env` command returns the same shape plus
  `"marker": <the FIXTURE_MARKER environment value, "" when unset>`.
- A `raw-text` command writes the line `canned output` to stdout.
- A `none` command writes nothing.
- The `own` command exits with the declared code `3` when its
  `--conflict` flag is given, and exits 0 otherwise; the result writes
  in both cases.
- The `sleep` command blocks until its context is cancelled or its
  `--seconds` elapse, then returns; it writes nothing and installs no
  signal handlers of its own.
- The error prefix (`errorPrefix`) is undeclared in every fixture, so
  diagnostics carry the command path alone.

## Running the reference suite

The reference runner is the Go library in the parent repository: it walks
this index, builds each fixture CLI in-process, and asserts every case.
The next stack stage wires that runner to this index; until it lands,
these fixtures are the target port authors build against.

## Writing a port runner

The evaluation loop is simple; no Go toolchain is required.

```
load index.json                              # located at conformance/index.json
base_dir = directory containing index.json   # i.e. conformance/
for each entry in index.fixtures:
    fixture = parse_json(base_dir / entry.path)
    validate that fixture.fixtureVersion == 1
    build your fixture CLI from fixture.declaration

    for each case in fixture.cases:
        if case.signal and no long-running command is declared:
            skip the case
        result = run(your_cli, case.argv, case.env or {}, case.signal or none)
        assert result.exitCode == case.expectedExitCode, case.id
        assert class(result.stdout) == case.stdout, case.id
        assert class(result.stderr) == case.stderr, case.id
        for pointer, expected in case.stdoutMatches or {}:
            assert resolve(parse_json(result.stdout), pointer) == expected, case.id
```

A case is a violation when the exit code, a stream class, or a pinned
pointer disagrees. Help wording is never pinned — compare classes, not
text.
