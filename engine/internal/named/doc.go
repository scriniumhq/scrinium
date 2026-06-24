// Package named is the pointer-free on-disk layout for system
// artifacts (ADR-85): manifests addressed by a NAME rather than by
// content hash, the name-addressed counterpart of the content-addressed
// engine/internal/cas. It is the single source of truth for where
// a system artifact lives and how its active version is chosen, shared by
// the callers that previously each carried their own copy of the rule:
// the systemstore facade (engine/systemstore), the bootstrap config path
// (engine/store/internal/storeconfig), and the lease primitive
// (engine/lease). Putting the layout here is what collapses that
// duplication into one mechanism.
//
// A system artifact is identified by a NAME — a flat, dot-separated
// key: "store.config", "store.agent.checkpoint.<ts>", "store.agent.gc.lease".
// The name maps deterministically to a flat file under the named root;
// each write of that name claims a new, monotonically increasing SEQ,
// stored as a dot-suffixed flat file (no per-artifact subdirectory):
//
//	named/<name>.<seq>     e.g. named/store.config.0000000001
//	                            named/store.agent.checkpoint.<ts>.0000000001
//
// The file at named/<name>.<seq> IS the (inline) manifest — system artifacts
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
package named
