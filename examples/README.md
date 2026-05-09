# Scrinium examples

Runnable programs that show how to embed Scrinium in your own code.
Each example is a self-contained `main.go` you can `go run` directly.

## Examples

| Example | What it shows | Lines |
|---------|--------------|-------|
| [hello](./hello) | Smallest possible program: init a store, put one file, read it back, close. | ~50 |
| [ingest](./ingest) | Batch ingest: scan a directory tree and put every file into a store. Demonstrates Put with options, namespace, error handling. | ~120 |
| [browse](./browse) | Read-only browser: open an existing store, iterate all artifacts, print summary stats. | ~80 |

## Running

Each example creates a temporary store under `/tmp` (override with `--store=...`).

```bash
cd examples

# Smallest end-to-end: open → put → get → close.
go run ./hello

# Ingest a directory tree.
go run ./ingest --src=/path/to/files --store=/tmp/my-store

# Browse what's inside a store.
go run ./browse --store=/tmp/my-store
```

## What each example uses

All three import the top-level [`scrinium`](https://pkg.go.dev/github.com/rkurbatov/scrinium)
package — the high-level wrapper that bundles store, index, view, and FSOps. They
also import the `domain` package for `Artifact`/`PutOptions`/`GetOptions` types.

The side-effect imports for [`driver/localfs`](../driver/localfs) and
[`index/sqlite`](../index/sqlite) live inside `scrinium` already, so examples
do not need to import them separately.

## What this module is

`examples/` is a separate Go module so it can demonstrate the minimum set of
imports needed for typical embedding tasks. The `replace` directive in `go.mod`
points at the parent `scrinium/` engine; that means a fresh `git clone` builds
the examples against the local source tree without any extra setup.

For a tagged release, the host can drop the `replace` and pin a specific
version of the engine.