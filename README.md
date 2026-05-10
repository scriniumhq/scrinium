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
│   ├── core/                # Store implementation
│   ├── domain/              # types (Manifest, Artifact, ...)
│   ├── driver/              # storage backends (localfs, ...)
│   ├── index/               # metadata index backends (sqlite, ...)
│   ├── plugin/              # encoders/decoders (zstd, aes-gcm, ...)
│   ├── projection/          # read-side: View, FSOps, fsmeta
│   ├── agent/, curator/, maintenance/   # workers
│   ├── errs/, event/        # cross-cutting types
│   └── internal/            # engine-private helpers
│
├── cmd/                     # reference binaries
│   ├── scrinium-fuse/       # FUSE mount (build tag: fuse)
│   ├── scrinium-webdav/     # WebDAV server
│   └── scrinium-webview/    # HTML browser
│
└── examples/                # example programs
    ├── hello/               # smallest open + put + get
    ├── ingest/              # batch ingest from a directory tree
    └── browse/              # read-only inspector
```

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
make ci                         # fmt + vet + test + fuzz-smoke
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

For full control over wiring, compose `engine/core`, `engine/projection`,
and friends directly. The top-level package is a convenience over them.

## License

TBD.