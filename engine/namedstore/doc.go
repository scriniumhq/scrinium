// Package namedstore is the pointer-free on-disk layout for system
// artifacts (ADR-85). It is the single source of truth for where a
// system artifact lives and how its active version is chosen, shared by
// the two callers that previously each carried their own copy of the
// rule: the SystemStore facade (engine/store) and the bootstrap config
// path (engine/store/internal/storeconfig). Putting the layout here is
// what collapses that duplication into one mechanism.
//
// A system artifact is identified by a NAME — a flat, dot-separated
// key: "store.config", "store.checkpoint.<ts>", "store.state.gc.lease".
// The name maps deterministically to a driver directory (system/<name>/);
// each write of that name lands in a new, monotonically increasing SEQ file inside
// that directory:
//
//	system/<name>/<seq>     e.g. system/config/00000000000000000003
//	                             system/scrub/cursor/00000000000000000012
//
// The file at <name>/<seq> IS the (inline) manifest — system artifacts
// are short and unique per write, so they carry their payload inline
// with an empty Pipeline (ContentHash == BlobRef). There is no separate
// blob file, no content-addressed manifests/ entry, and no StoreIndex
// row: system artifacts live ONLY here, in their own address space.
//
// This replaces the previous mutable-pointer model (a "<name> → digest"
// file plus a content-addressed manifest). Dropping the pointer has
// three consequences:
//
//   - Active version = max(seq), discovered by reading the directory.
//     No pointer to flip, so no window in which the pointer and the
//     file it names disagree. (This is already the StoreConfig
//     activation model — see the concurrency model §3.1 — so the
//     config path stops being a special case.)
//   - A new version is published by CLAIMING the next seq with an
//     exclusive create (driver.WithExclusive — the Layer-1 atomic
//     commit primitive). Two racing writers cannot occupy one seq: the
//     loser gets errs.ErrAlreadyExists and re-reads max(seq).
//   - Rollback is "write a copy as the new max(seq)"; GC is keep-N by
//     version (Prune), never by ref-count — system artifacts are
//     outside the content-addressed GC regime.
//
// Integrity: with no index row to drive the scrub schedule, system
// artifacts are verified ON READ. Load re-hashes the inline payload
// against the manifest's embedded ContentHash, so a silently corrupted
// file is rejected at the point of use without any background scrub
// pass. Config is read at every store-open and the few other system
// names on each touch — frequent enough that verify-on-read is the
// whole integrity story.
package namedstore
