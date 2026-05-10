# cmd/

Reference binaries that demonstrate hosting Scrinium under different
surfaces. Each is a thin adapter over the top-level `scrinium` package,
adding only the surface-specific code (FUSE inode tree, WebDAV adapter,
HTTP handler).

- `scrinium-fuse` — POSIX filesystem mount on Linux/macOS via FUSE.
  Build tag `fuse`.
- `scrinium-webdav` — cross-platform WebDAV server.
- `scrinium-webview` — read-only HTML browser for inspecting a store.

Install from source:

```bash
go install scrinium.dev/cmd/scrinium-webdav@latest
go install scrinium.dev/cmd/scrinium-fuse@latest    # Linux/macOS only
go install scrinium.dev/cmd/scrinium-webview@latest
```