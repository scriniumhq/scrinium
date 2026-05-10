# Scrinium

A content-addressable storage engine in Go, with a high-level API and
reference applications (FUSE, WebDAV, HTML browser).

## Status

In development. The on-disk format and public API may change.

## Layout

This repository is a Go workspace with three modules:

```
scrinium/
├── go.work                  # workspace
│
├── go.mod                   # engine module: scrinium.dev
├── *.go                     # high-level wrapper API: scrinium.Open / scrinium.Init
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
├── cmd/                     # reference binaries module
│   ├── go.mod               # scrinium.dev/cmd
│   ├── scrinium-fuse/       # FUSE mount (build tag: fuse)
│   ├── scrinium-webdav/     # WebDAV server
│   └── scrinium-webview/    # HTML browser
│
└── examples/                # example programs module
    ├── go.mod               # scrinium.dev/examples
    ├── hello/               # smallest open + put + get
    ├── ingest/               # batch ingest from a directory tree
    └── browse/              # read-only inspector
```

## Quick start

The smallest program — open a fresh store, put one artifact, read it back:

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
  cfg := scrinium.DefaultConfig()
  cfg.Store = "file:///tmp/my-store"

  s, _, err := scrinium.Init(context.Background(), cfg)
  if err != nil {
    log.Fatal(err)
  }
  defer s.Close()

  id, err := s.Store.Put(context.Background(),
    domain.Artifact{Payload: bytes.NewReader([]byte("hello"))},
    domain.PutOptions{Namespace: "demo"},
  )
  if err != nil {
    log.Fatal(err)
  }

  rh, _ := s.Store.Get(context.Background(), id, domain.GetOptions{})
  defer rh.Close()
  body, _ := io.ReadAll(rh)
  log.Printf("read back: %s", body)
}
```

See `examples/` for runnable variations (`go run ./hello`, `./ingest`, `./browse`).

## Building

The repo uses a Go workspace, so `go build` and `go test` from the root
operate across all three modules:

```bash
go build ./...                  # build everything
go test ./...                   # test everything (FUSE included on Linux/macOS)
make ci                         # fmt + vet + test + fuzz-smoke
```

For a single module:

```bash
cd cmd && go build ./...        # just the binaries
cd examples && go run ./hello   # just an example
```

`make tidy` runs `go mod tidy` in each module separately.

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

- `scrinium.Init(ctx, cfg)` — create a fresh store, returns `*Scrinium`
  with everything wired (Store, Index, View, FSOps).
- `scrinium.Open(ctx, cfg)` — open an existing store the same way.
- `(*Scrinium).Close()` — wipe secrets, release resources.

For full control over wiring, compose `engine/core`, `engine/projection`,
and friends directly. The top-level package is a convenience over them.

## License

TBD.