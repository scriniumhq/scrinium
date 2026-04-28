# cmd/

Entry points for Scrinium executables. Each is a thin adapter
over the projection package, gated by a build tag so the engine
itself stays free of the platform-specific dependencies.

- `cmd/scrinium-fuse` — FUSE mount, build tag `fuse`.
- `cmd/scrinium-webdav` — WebDAV server, build tag `webdav`.