# cmd/

Reference binaries that demonstrate hosting Scrinium under different
surfaces. Each is a thin adapter over the top-level `scrinium` package,
adding only the surface-specific code (FUSE inode tree, WebDAV adapter,
HTTP handler).

- `scrinium-fuse` — POSIX filesystem mount on Linux/macOS via FUSE.
  Build tag `fuse`.
- `scrinium-webdav` — cross-platform WebDAV server.
- `scrinium-webview` — read-only HTML browser for inspecting a store.

This is a separate Go module (`github.com/rkurbatov/scrinium/cmd`) so the
engine itself stays free of binary-only dependencies (go-fuse, x/net,
yaml). The `replace` directive in `cmd/go.mod` points at the parent
engine for local development; tagged releases pin a specific version.

Install from source:

```bash
go install github.com/rkurbatov/scrinium/cmd/scrinium-webdav@latest
go install github.com/rkurbatov/scrinium/cmd/scrinium-fuse@latest    # Linux/macOS only
go install github.com/rkurbatov/scrinium/cmd/scrinium-webview@latest
```