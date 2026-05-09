// Package scrinium is the high-level entry point for embedding
// Scrinium into an application. It bundles the bootstrap pipeline
// every reference binary uses (driver dial, index dial, store
// open, view backfill, fsops assembly) into two functions:
//
//	scrinium.Init  — create a fresh store on disk
//	scrinium.Open  — open an existing store and return a runtime
//
// The returned *Scrinium owns the Store, the StoreIndex, the
// projection View, the FSOps facade, and the boot-unique
// MountSession. Surfaces (FUSE, WebDAV, HTTP, gRPC, custom
// protocols) consume those fields directly; this package does
// not impose a surface-specific shape.
//
// # Why a top-level package
//
// Scrinium has many supporting packages (core, domain, driver,
// index, plugin, projection, agent, curator, ...). Each is
// reusable on its own; together they form the engine. The
// top-level scrinium package is a thin opinion layer over them:
// it picks reasonable defaults (sqlite index next to file://
// stores, sha256 hashing, fsmeta path resolution, fsindex
// extension), wires fail-safe error paths (deferred cleanup of
// partially-opened resources), and zeroizes secrets on shutdown.
//
// Hosts that want full control over wiring should compose
// core.OpenStore and friends directly; this package is a
// convenience.
//
// # Lifecycle
//
//  1. Build a scrinium.Config (use scrinium.DefaultConfig and
//     edit the fields you care about).
//  2. Call scrinium.Open(ctx, cfg) — opens store, index, view, fsops.
//  3. Use the returned *Scrinium until shutdown.
//  4. Call (*Scrinium).Close — wipes secrets, closes resources.
//
// # First-time use
//
// scrinium.Init creates a new store at a fresh location, writes
// the descriptor and system.config, and returns a *Scrinium that
// is already open and ready to use. For encrypted stores it
// produces a recovery kit that the host MUST persist (out of
// band) before the first Close — it is the only path back into
// the store if the passphrase is lost.
//
// # Examples
//
// See the examples/ module in the Scrinium repository for full
// runnable programs (hello, ingest, browse).
package scrinium
