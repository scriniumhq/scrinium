package core

import "scrinium.dev/engine/coreapi"

// system_store_options.go — option constructors for SystemStore.Put.
// The SystemPutOption contract and the SystemPutConfig it populates
// live in coreapi (with the SystemStore interface); this file holds
// the concrete options the core implementation offers.

// withoutIndexOption is the applier returned by WithoutIndex.
type withoutIndexOption struct{}

func (withoutIndexOption) ApplySystemPut(c *coreapi.SystemPutConfig) {
	c.SkipIndex = true
}

// WithoutIndex skips indexing the manifest in StoreIndex. Used for
// artifacts whose presence in the index would be either redundant
// or actively harmful — most notably index snapshots themselves
// (indexing a snapshot of the index inside the same index creates
// an asymmetry where the snapshot row points at a manifest that
// only exists after the snapshot was taken).
//
// Default (no option) indexes the artifact. This is the right
// choice for cursors and config — small, frequently-read
// artifacts where the index access path is cheaper than reading
// the manifest file twice (once for the pointer's referent, once
// for the artifact body).
func WithoutIndex() coreapi.SystemPutOption {
	return withoutIndexOption{}
}
