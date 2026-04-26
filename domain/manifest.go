package domain

import (
	"encoding/json"
	"io"
	"time"
)

// Artifact is the abstraction at the system boundary (input/output).
// It consists of a byte stream (Payload) and a business context
// (Metadata). The engine treats Metadata as an opaque blob —
// parsing tags and paths is strictly the responsibility of the host
// application.
type Artifact struct {
	Payload  io.Reader
	Metadata json.RawMessage
}

// ManifestType is the role of a Manifest.
type ManifestType string

const (
	ManifestTypeBlob ManifestType = "blob"
	ManifestTypeTOC  ManifestType = "toc"
	ManifestTypePack ManifestType = "pack"
)

// LayoutHeader is a service attribute inside a Manifest that
// "freezes" the physical-projection rules applied at write time.
type LayoutHeader struct {
	BlobStorage string
}

// PipelineStage is a single transformation stage in the Pipeline.
type PipelineStage struct {
	Algorithm string
	Hash      string
	IV        []byte
}

// ManifestSystemFlags is the system block of a Manifest. Present
// only for type: "pack".
type ManifestSystemFlags struct {
	TOCOffset int64
	TOCSize   int64
}

// Manifest is the logical passport of an Artifact.
type Manifest struct {
	ArtifactID ArtifactID
	Type       ManifestType
	Namespace  string
	SessionID  string
	CreatedAt  time.Time

	ContentHash  ContentHash
	OriginalSize int64
	BlobRef      BlobRef
	LayoutHeader LayoutHeader
	Pipeline     []PipelineStage
	InlineBlob   []byte
	ExternalURI  string

	RetentionUntil time.Time
	KeyID          string

	SystemFlags ManifestSystemFlags
	Metadata    json.RawMessage
}
