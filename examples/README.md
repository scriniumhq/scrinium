# Scrinium examples

Runnable programs that show how to embed Scrinium in your own code.
Each example is a self-contained `main.go` you can `go run` directly.

## Examples

| Example | What it shows | Lines |
|---------|--------------|-------|
| [hello](./hello) | Smallest possible program: init a store, put one file, read it back, close. | ~50 |
| [browse](./browse) | Read-only browser: open an existing store, iterate all artifacts, print summary stats. | ~80 |

## Running

Each example creates a temporary store under `/tmp` (override with `--store=...`).

```bash
# Smallest end-to-end: open → put → get → close.
go run ./examples/hello

# Ingest a directory tree.
go run ./examples/ingest --src=/path/to/files --store=/tmp/my-store

# Browse what's inside a store.
go run ./examples/browse --store=/tmp/my-store
```

## What each example uses

All three import the top-level [`scrinium`](https://pkg.go.dev/scrinium.dev)
package — the high-level wrapper that bundles store, index, view, and FSOps. They
also import the `domain` package for `Artifact`/`PutOptions`/`GetOptions` types.

The side-effect imports for [`driver/localfs`](../driver/localfs) and
[`index/sqlite`](../index/sqlite) live inside `scrinium` already, so examples
do not need to import them separately.
