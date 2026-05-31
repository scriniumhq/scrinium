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

// RoutingHints are policy hints for curator.RoutingFunc.
type RoutingHints struct {
	ContentType string
	Source      string
	Attributes  map[string]string
}

// PutOptions is the call context for Store.Put.
type PutOptions struct {
	SessionID      SessionID
	Namespace      string
	BlobType       BlobType
	RetentionUntil time.Time
	Routing        RoutingHints
}

// GetOptions is the call context for Store.Get.
type GetOptions struct {
	AllowColdRead bool
}
