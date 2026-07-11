package index

import (
	"context"

	"scrinium.dev/domain"
)

// DuplicateHandleAuditor is the optional capability of a StoreIndex that
// can cheaply enumerate handles carrying more than one live manifest row.
//
// The manifests_artifact index is deliberately NON-UNIQUE (decision R6):
// a form migration may transiently hold two rows per handle. Outside an
// active migration a duplicate is a write-path bug the schema will not
// catch, so the Scrub Agent probes this capability once per cycle and
// surfaces hits (Warn + ScrubStats). Optional by-assertion, like every
// index capability: a backend without a cheap GROUP BY simply does not
// implement it and the check is skipped.
type DuplicateHandleAuditor interface {
	ListDuplicateHandles(ctx context.Context) ([]domain.ArtifactID, error)
}

// CheckpointWriter is the optional capability of a StoreIndex that can
// serialize its full state into a self-contained checkpoint file — a
// point-in-time copy the RebuildIndexAgent can later restore and replay
// forward (see the matching CheckpointRestorer).
//
// It is deliberately NOT part of the mandatory StoreIndex contract.
// Backends whose durability and backup are owned by an external system
// (e.g. Postgres) do not implement it, and the checkpoint agent simply does
// not run for them; recovery for such backends is a full scan (or the
// backend's own restore tooling). The sqlite backend implements it via
// SQLite's online VACUUM INTO.
//
// destPath must name a non-existent file on a local filesystem; the writer
// must not overwrite an existing file — a collision signals two writers
// racing the same path, which is a bug to surface, not mask.
type CheckpointWriter interface {
	WriteCheckpoint(ctx context.Context, destPath string) error
}

// CheckpointRestorer is the optional capability of a StoreIndex that can load
// a checkpoint file (produced by a CheckpointWriter) back into itself. The
// rebuild fast-path uses it to populate a fresh index from a recent
// checkpoint before replaying the tail of manifests written since. Backends
// that do not implement CheckpointWriter do not implement this either.
//
// srcPath must name an existing checkpoint file on a local filesystem. The
// restore targets a freshly created, empty index; the implementation migrates
// the checkpoint forward to the running schema and refuses one written by
// newer code than the running binary.
type CheckpointRestorer interface {
	RestoreCheckpoint(ctx context.Context, srcPath string) error
}

// UsrIndexingSwitch is the optional capability of toggling the global
// usr-pocket indexing gate the index holds in memory. The Store owns the
// durable switch (a keep=0 system-artifact cell, ADR-104 §6) and pushes its
// value here on open and on change; the index then reads the in-memory flag
// on its hot projection/query paths. Indexes that do not project the usr
// pocket need not implement it.
type UsrIndexingSwitch interface {
	SetUsrIndexing(on bool)
}

// Token is an opaque, monotonic change marker for an index that supports the
// synchronization capability (ADR-106). It advances on every committed
// mutation — IndexManifest and DeleteManifest alike — and never moves
// backwards: it is the index's commit counter, NOT max(csn) over manifest
// rows, which would regress when a delete removes the highest-csn row. The
// value is opaque; only ordering (>) and equality are meaningful. SQLite
// issues it from a counter row; Postgres would map a sequence/txid onto it.
type Token uint64

// Change is one manifest that moved at the given change-sequence.
type Change struct {
	Digest domain.ManifestDigest // the manifest that changed
	CSN    Token                 // its change-sequence
}

// Delta is the result of SyncSource.Since: the manifests that changed in
// (cursor, now], the cursor to resume from, and whether history was pruned
// past the caller's cursor.
//
// Hard deletes are not enumerated as Changes — the row is gone, so Since
// cannot surface the deleted digest by csn. Instead Gapped reports that a
// delete pruned history at or after the caller's cursor; a gapped consumer
// re-derives by a full Walk rather than applying the partial delta (ADR-106).
type Delta struct {
	Changes []Change // changed manifests in (cursor, now], ordered by csn
	Next    Token    // cursor to pass to the next Since call
	Gapped  bool     // history pruned past cursor → consumer must full-Walk
}

// SyncSource is the optional pull capability that lets a consumer learn about
// changes made by other clients sharing the same monostore. It is
// deliberately NOT part of the mandatory StoreIndex contract: an index that
// does not support shared access simply does not implement it, and such a
// deployment is single-client. Discovered by assertion — idx.(SyncSource).
//
// Token is the cheap "did anything change?" probe (one counter read); Since
// pulls the actual changes. Pull is the source of truth. Convergent consumers
// (the projection view, the system-artifact cache) use Token alone; a
// delta/queue consumer uses Since with a persisted cursor. The full model
// lives in 2. Internals/07 Multiclient Synchronization.
type SyncSource interface {
	Token(ctx context.Context) (Token, error)
	Since(ctx context.Context, cursor Token) (Delta, error)
}

// SyncWaiter is the optional push capability layered over SyncSource: it
// blocks until the index moves past `after` (or ctx is cancelled), so a
// consumer need not busy-poll Token. Push is NOT the source of truth — it
// lives only until a connection drops; correctness rests on the Since
// catch-up. SQLite implements it by polling its counter; Postgres would use
// LISTEN/NOTIFY, Redis Pub/Sub. An index may implement SyncSource without
// SyncWaiter (the consumer then polls Token itself).
type SyncWaiter interface {
	Wait(ctx context.Context, after Token) (Token, error)
}

// ManifestResolver is the optional capability the projection's incremental
// convergence needs alongside SyncSource (ADR-107): it reconstructs a full
// manifest from a digest reported by Since, so a consumer can apply just the
// changed artifacts instead of re-walking the whole index. Discovered by
// assertion — idx.(ManifestResolver) — and reconstructs exactly what
// IterateManifests yields, so a resolved manifest is indistinguishable from a
// walked one.
//
// ok is false when no manifest carries the digest (e.g. it was pruned between
// the Since read and this resolve); the caller treats that as "nothing to
// apply", since a hard delete is already covered by Delta.Gapped.
type ManifestResolver interface {
	ManifestByDigest(ctx context.Context, digest domain.ManifestDigest) (domain.Manifest, bool, error)
}

// ManifestCounter is the optional capability of a StoreIndex that can report
// the number of user manifests (artifact_id present) without materialising
// them. Capacity uses it to avoid deserialising every manifest just to count;
// backends that cannot answer cheaply (or at all) simply do not implement it,
// and the caller falls back to a full IterateManifests walk. The count must
// match exactly what IterateManifests would yield — same user-manifest filter.
type ManifestCounter interface {
	CountManifests(ctx context.Context) (int64, error)
}
