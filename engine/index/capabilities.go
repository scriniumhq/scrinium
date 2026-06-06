package index

import "context"

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

// CheckpointInspector is the optional capability of reading a single
// store_meta value from a checkpoint file WITHOUT restoring it. It lets the
// Store layer verify a checkpoint's recorded identity (the descriptor blob)
// before deciding to restore. The value is returned raw; interpreting it is
// the caller's concern. An absent key yields ("", nil), distinguishing
// "the checkpoint records no such metadata" from a read failure.
type CheckpointInspector interface {
	CheckpointMeta(ctx context.Context, srcPath, key string) (string, error)
}
