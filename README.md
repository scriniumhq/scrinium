# Scrinium

A content-addressable storage engine in Go, with a high-level front
door and reference applications (FUSE, WebDAV, HTML browser).

## Status

In development. The on-disk format and public API may change.

## Layout

Single Go module (`scrinium.dev`):

```
scrinium/
├── go.mod                   # module scrinium.dev
├── scrinium.go              # front door: Open / Build / Load*, *ScriniumClient
│
├── domain/                  # core types (Manifest, Artifact, config, options, IDs)
├── errs/                    # flat sentinel-error package (errors.Is targets)
├── event/                   # flat event bus + payloads; reserved type-string
│                            #   namespaces: store.* agent.* index.* projection.*
│
├── engine/                  # the engine
│   ├── store/               # Store implementation (Put/Get/Delete/Walk, Admin, lifecycle)
│   ├── artifact/            # manifest encode/decode, on-disk paths, header/crypto
│   ├── driver/              # storage backends (localfs; faulty for tests; s3 stub)
│   ├── index/               # metadata index backends (sqlite; postgres stub) + extensions
│   ├── pipeline/            # blob transform stages (zstd, aes-gcm; segmented AEAD)
│   ├── wrapper/             # composition decorators (bundler, chunker, multistore)
│   ├── hashing/             # content-hash registry
│   ├── agent/               # background & maintenance workers (single Agent contract)
│   └── internal/            # engine-private helpers (aead, lease, timefmt, ...)
│
├── projection/              # read/write filesystem projection (View, FSOps, vfs, pathx)
├── internal/                # assembly (the wiring) + secretref
├── testutil/                # shared test fixtures and harnesses (see TESTING.md)
│
├── cmd/                     # reference binaries
│   ├── scrinium-fuse/       # FUSE mount (build tag: fuse)
│   ├── scrinium-webdav/     # WebDAV server
│   └── scrinium-webview/    # read-only HTML browser
│
└── examples/                # runnable programs
    ├── hello/               # smallest open + put + get (front door)
    ├── hello-manual/        # the same store assembled from primitives, no front door
    ├── ingest/              # batch ingest from a directory tree
    └── browse/              # read-only inspector
```

Some backends and workers are intentionally stubs until their
milestone, returning `errs.ErrNotImplemented`: the s3 driver, the
postgres index, and the `ingester` / `ejector` agents. The implemented
agents — `gc`, `scrub`, `snapshot`, and index `rebuild` — share one
lifecycle contract (`agent.Agent`: `Validate` then `Run`) and register
through a package-level factory registry; the host or a scheduler
drives them. See `TESTING.md` for how the stubs affect coverage.

## Quick start

The smallest program — open or create a store, put one artifact, read
it back. Backends register by blank import (ADR-63), as with the
drivers in `database/sql`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	scrinium "scrinium.dev"

	// Pull in the backends this program uses.
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"
)

func main() {
	ctx := context.Background()

	// Open assembles a store from a driver URI, creating it if absent.
	// A *ScriniumClient IS a store: Put/Get are called on it directly,
	// and a single Close releases everything it assembled.
	s, err := scrinium.Open(ctx, "file:///tmp/scrinium-hello")
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	id, err := s.Put(ctx,
		scrinium.Artifact{Payload: strings.NewReader("hello, scrinium!\n")},
		scrinium.WithNamespace("demo"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("stored: %s\n", id)

	rh, err := s.Get(ctx, id)
	if err != nil {
		log.Fatal(err)
	}
	defer rh.Close()

	body, _ := io.ReadAll(rh)
	fmt.Printf("read back: %s", body)
}
```

See `examples/` for runnable variations (`go run ./examples/hello`,
`./ingest`, `./browse`), and `examples/hello-manual` for the same store
assembled from primitives with no front door at all.

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
go run ./cmd/scrinium-webdav serve --config=store.yaml --listen=:8080
```

## Reference binaries

Pre-built CLI applications under `cmd/` demonstrate three integrations:

- `scrinium-fuse` — POSIX filesystem on Linux/macOS via FUSE.
- `scrinium-webdav` — cross-platform WebDAV server.
- `scrinium-webview` — read-only HTML browser for inspecting a store.

Each takes a `serve` subcommand with a `--config` YAML file. Install
from source:

```bash
go install scrinium.dev/cmd/scrinium-webdav@latest
```

## Embedding

For applications that host Scrinium directly, use the top-level
`scrinium` package. Every entry point returns a `*ScriniumClient`,
which embeds `store.Store` — `Put`/`Get`/`Walk` and the `Admin()` facet
are called on it directly — and carries the optional `Projection`, the
assembly `Info`, and the `MountSession`.

- `scrinium.Open(ctx, driverURI, opts...)` — assemble from a single
  driver URI, creating it if absent. The simplest entry point: no
  config document, no projection.
- `scrinium.Build(ctx, cfg, opts...)` — assemble from a programmatic
  `Config` (driver + index + policy, and an optional projection).
- `scrinium.LoadYAML` / `LoadInitYAML` / `LoadOrInitYAML` (and the
  `*JSON` mirrors) — assemble from a configuration document, the usual
  shape for a deployed service an operator configures by file.
- `scrinium.WithMode(ModeOpen | ModeInit | ModeOpenOrInit)` — choose
  open/init behaviour; the default is `ModeOpenOrInit`.
- `(*ScriniumClient).RecoveryKit() ([]byte, bool)` — on a freshly
  initialised encrypted store, returns the recovery kit and `true`. The
  host MUST persist it: it is the only way back in if the passphrase is
  lost. Pair it with `Info.Created`.
- `(*ScriniumClient).Close()` — cascades, releasing the projection, the
  store, and the index together, and wipes secrets. Idempotent.

Production daemons typically separate "init" and "serve" so an operator
can audit when a brand-new store is being created.

For full control over the wiring, compose `engine/store`,
`engine/index`, `projection`, and friends directly — the top-level
package is a convenience over them. `examples/hello-manual` shows the
hand-assembled path.

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Contributions are accepted under the same license. Every commit
must include a Developer Certificate of Origin sign-off — see
[CONTRIBUTING.md](CONTRIBUTING.md) for details.