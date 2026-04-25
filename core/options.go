package core

import "time"

// BlobType is the type of a blob; it determines the target write
// directory.
type BlobType string

const (
	// BlobTypeRegular — a regular blob, /blobs/. Default for client
	// code.
	BlobTypeRegular BlobType = "Regular"

	// BlobTypePack — a .pack volume, /packs/. Used by bundler.Wrapper.
	BlobTypePack BlobType = "Pack"

	// BlobTypeChunk — an anonymous chunk, /chunks/. Used by
	// chunker.Wrapper.
	BlobTypeChunk BlobType = "Chunk"
)

// RoutingHints are policy hints for curator.RoutingFunc when
// selecting target Stores on the write path. Without Curator they
// are ignored. They are not persisted in the manifest: at Drain time
// they are reconstructed by the MetadataRouter from Manifest.Metadata.
type RoutingHints struct {
	ContentType string
	Source      string
	Attributes  map[string]string
}

// PutOptions is the call context for Store.Put. All fields are
// optional except ExternalURI, which is required when
// BlobStorage: ExternalRef is in effect. A zero PutOptions{} is
// allowed and means "default namespace, no session, no retention,
// regular blob".
type PutOptions struct {
	SessionID      string
	Namespace      string
	ExternalURI    string
	BlobType       BlobType
	RetentionUntil time.Time // zero value = no retention protection
	Routing        RoutingHints
}

// GetOptions is the call context for Store.Get. The minimal valid
// call is GetOptions{}.
type GetOptions struct {
	// AllowColdRead allows reads from archival sources (Backup with
	// ReadPolicy: Never, Target with ReadCost: High). Relevant only
	// when reading through Curator.
	AllowColdRead bool
}
