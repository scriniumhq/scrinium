// Package daemon is the shared bootstrap library every scrinium
// binary builds on. It owns the live resources a Scrinium
// daemon needs — the Store, the Index, the projection View,
// the FSOps facade — and the policy/config that wires them
// together.
//
// Each cmd binary (scrinium-webdav, scrinium-fuse, the future
// scrinium-webview, plus any composite) opens a Daemon at
// startup and consumes its fields. The cmd contributes its own
// surface (HTTP handler, FUSE mount, etc.) on top.
//
// Lifecycle:
//
//  1. cmd loads its config and the embedded daemon.Config.
//  2. cmd calls daemon.Open(ctx, cfg) — opens store/index,
//     builds View and FSOps.
//  3. cmd starts its surface, passing the *Daemon down.
//  4. On shutdown, cmd calls Daemon.Close.
//
// The split between Daemon and cmd lets multiple binaries
// reuse the same bootstrap without duplicating it. Surface-
// specific config (listen address, mount point, etc.) stays
// in each cmd package.
//
// Internal because this is not part of the public Scrinium
// API — external consumers compose Store/Index/View directly
// from the public packages.
package daemon
