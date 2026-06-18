package domain

import "time"

// BlobType is the type of a blob; it determines the target write
// directory.
type BlobType string

const (
	BlobTypeRegular BlobType = "Regular"
	BlobTypePack    BlobType = "Pack"
	BlobTypeChunk   BlobType = "Chunk"
)

// RoutingHints are policy hints for multistore.RoutingFunc.
type RoutingHints struct {
	ContentType string
	Source      string
	Attributes  map[string]string
}

// PutOptions is the call context for Store.Put.
type PutOptions struct {
	SessionID      SessionID
	BlobType       BlobType
	RetentionUntil time.Time
	Routing        RoutingHints

	// ExtHints is the generic, opaque per-call channel from a client to
	// the behavior wrappers, keyed by extension name. The core NEVER reads
	// it — it carries the map through Put to the wrappers, which alone
	// interpret their own key (e.g. a namespace name an extension wrapper
	// resolves and stamps into Ext). This is how a client passes an
	// extension a per-call value without the core learning that
	// extension's vocabulary.
	ExtHints map[string]string
}

// GetOptions is the call context for Store.Get.
type GetOptions struct {
	AllowColdRead bool
}
