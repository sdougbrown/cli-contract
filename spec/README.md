# cli-contract spec

The language-neutral conformance specification for the CLI behavior
contract: the rules any conforming CLI follows, whether it embeds the
[cli-contract](https://github.com/sdougbrown/cli-contract) library or
implements the behavior by hand.

This directory provides three artifacts:

- **`spec.md`** — the normative v1 behavior contract: the library-owned
  conventions every conforming CLI follows, and the behavior an app
  declares and then honors.
- **`cli-contract.schema.json`** — a JSON Schema (draft-07) that
  validates a CLI's declaration document. Editors can use it for
  autocomplete and inline validation without a Go toolchain.
- **`conformance/`** (from spec v1 stage 2) — plain JSON fixtures. Any
  implementation — the Go library, a Rust or Python port, a hand-written
  CLI — consumes the same files to verify behavioral correctness.

A **black-box checker** (`cli-contract check <binary>`, built beside the
spec it enforces) derives cases from a target's declared surface, runs
them against the binary, and reports every violation. It works on CLIs
that do not use the library; cli-registry can run it before generating
or trusting a manifest.

## Versioning

The spec uses semver tags (`spec-v1.0.0`). A change that requires
implementations to update — a new convention, a fixture-contract change,
a removed declaration field — bumps the tag. The [v1 discrepancy
ledger](v1-discrepancy-ledger.md) freezes the places where the written
rule and the reference Go implementation disagree.
