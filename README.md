# Scrinium

A content-addressable storage engine in Go, with a high-level API and
reference applications (FUSE, WebDAV, HTML browser).

## Status

In development. The on-disk format and public API may change.

## Layout

Single Go module (`scrinium.dev`):

```
scrinium/
├── go.mod                   # scrinium.dev
├── *.go                     # high-level wrapper API: scrinium.Open / scrinium.Init
│
├── engine/                  # the engine itself
│   ├── store/               # Store implementation (Put/Get/Delete/Walk, lifecycle)
│   ├── domain/              # types (Manifest, Artifact, config, ...)
│   ├── artifact/            # manifest codec, on-disk paths, header/crypto
│   ├── driver/              # storage backends (localfs; faulty for tests; s3 stub)
│   ├── index/               # metadata index backends (sqlite; postgres stub)
│   ├── pipeline/            # blob transform stages (stage/zstd, stage/aesgcm; segaead)
│   ├── projection/          # read-side: View, FSOps, fsmeta, fsindex, vfs
│   ├── wrapper/             # composition helpers (bundler, chunker, host, multistore)
│   ├── hashing/             # content-hash registry
│   ├── agent/, curator/, maintenance/   # workers (agent: interfaces, impl in progress)
│   ├── errs/, event/        # cross-cutting types
│   └── internal/            # engine-private helpers (aead, timefmt, uriresolve)
│
├── cmd/                     # reference binaries
│   ├── scrinium-fuse/       # FUSE mount (build tag: fuse)
│   ├── scrinium-webdav/     # WebDAV server
│   └── scrinium-webview/    # HTML browser
│
├── internal/testutil/       # shared test fixtures (see TESTING.md)
│
└── examples/                # example programs
    ├── hello/               # smallest open + put + get
    ├── ingest/              # batch ingest from a directory tree
    └── browse/              # read-only inspector
```

Some backends are intentionally stubs until their milestone: the s3
driver and the postgres index return `ErrNotImplemented`, and the
`agent` workers (gc, scrub, ingester, ejector) are interfaces and
constructors with implementations still in progress. See `TESTING.md`
for how this affects test coverage.

## Quick start

The smallest program — open or create a store, put one artifact, read it back:

```go
package main

import (
  "bytes"
  "context"
  "io"
  "log"

  scrinium "scrinium.dev"
  "scrinium.dev/engine/domain"
)

func main() {
  ctx := context.Background()

  cfg := scrinium.DefaultConfig()
  cfg.Store = "file:///tmp/my-store"

  // OpenOrInit opens the store if it exists, initialises it
  // otherwise — and surfaces real errors (bad URI, permissions)
  // instead of silently reinitialising on top.
  s, _, _, err := scrinium.OpenOrInit(ctx, cfg)
  if err != nil {
    log.Fatal(err)
  }
  defer s.Close()

  id, err := s.Store.Put(ctx,
    domain.Artifact{Payload: bytes.NewReader([]byte("hello"))},
    domain.PutOptions{Namespace: "demo"},
  )
  if err != nil {
    log.Fatal(err)
  }

  rh, _ := s.Store.Get(ctx, id, domain.GetOptions{})
  defer rh.Close()
  body, _ := io.ReadAll(rh)
  log.Printf("read back: %s", body)
}
```

See `examples/` for runnable variations (`go run ./hello`, `./ingest`, `./browse`).

## Building

```bash
go build ./...                  # build everything
go test ./...                   # test everything (FUSE included on Linux/macOS)
make ci                         # fmt-check + vet + test + fuzz-smoke
```

`make help` lists the full set of targets (tests, fuzzing, benchmarks,
smoke runs). A few worth knowing:

```bash
make test-pkg P=engine/store    # test a single package
RACE=1 make test                # with the race detector
make fuzz-smoke                 # seed-corpus pass over every Fuzz*
make smoke                      # long-running million-files smoke
make bench-cmp                  # benchmarks vs the committed baseline
```

To run a single example or binary directly:

```bash
go run ./examples/hello
go run ./cmd/scrinium-webdav --store=/tmp/store --listen=:8080
```

## Reference binaries

Pre-built CLI applications under `cmd/` demonstrate three integrations:

- `scrinium-fuse` — POSIX filesystem on Linux/macOS via FUSE.
- `scrinium-webdav` — cross-platform WebDAV server.
- `scrinium-webview` — read-only HTML browser for inspecting a store.

Install from source:

```bash
go install scrinium.dev/cmd/scrinium-webdav@latest
```

## Embedding

For applications that want to host Scrinium directly, use the top-level
`scrinium` package:

- `scrinium.OpenOrInit(ctx, cfg)` — open or create. Convenient for
  examples and single-binary tools; returns a `created` flag so the
  host knows when a recovery kit has been produced.
- `scrinium.Open(ctx, cfg)` — open an existing store. Returns
  `errs.ErrStoreNotFound` (which bridges to `fs.ErrNotExist`) if no
  store has been initialised at the location.
- `scrinium.Init(ctx, cfg)` — create a fresh store. Returns the
  recovery kit on encrypted stores; the host MUST persist it.
- `(*Scrinium).Close()` — wipe secrets, release resources.
  Idempotent.

Production daemons typically separate "init" and "serve" subcommands
so an operator can audit when a brand-new store is being created.

For full control over wiring, compose `engine/store`,
`engine/projection`, and friends directly. The top-level package is a
convenience over them.

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Contributions are accepted under the same license. Every commit
must include a Developer Certificate of Origin sign-off — see
[CONTRIBUTING.md](CONTRIBUTING.md) for details.