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
