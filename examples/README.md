# Scrinium examples

Runnable programs that show how to embed Scrinium in your own code.
Each example is a self-contained `main.go` you can `go run` directly.

## Examples

| Example | What it shows | Lines |
|---------|--------------|-------|
| [hello](./hello) | Smallest possible program: init a store, put one file, read it back, close. | ~50 |
| [hello-manual](./hello-manual) | The same store assembled from primitives, with no front door — driver + index + store wired by hand. | ~90 |
| [browse](./browse) | Read-only browser: open an existing store, iterate all artifacts, print summary stats. | ~80 |

## Running

Each example creates a temporary store under `/tmp` (override with `--store=...`).

```bash
# Smallest end-to-end: open → put → get → close.
go run ./examples/hello

# The same store, assembled by hand with no front door.
go run ./examples/hello-manual

# Browse what's inside a store.
go run ./examples/browse --store=/tmp/my-store
```

## What each example uses

`hello` and `browse` import the top-level [`scrinium`](https://pkg.go.dev/scrinium.dev)
package — the high-level wrapper that bundles store, index, view, and FSOps — plus
the `domain` package for `Artifact`/`PutOptions`/`GetOptions` types. `hello-manual`
deliberately skips the wrapper and assembles the same store from primitives (driver,
index, store) to show what the front door hides.

The side-effect imports for [`driver/localfs`](../driver/localfs) and
[`index/sqlite`](../index/sqlite) live inside `scrinium` already, so the wrapper-based
examples do not need to import them separately; `hello-manual`, which bypasses the
wrapper, registers them itself.