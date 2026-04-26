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
	// ArtifactID is the in-memory identity of the manifest. It is
	// NOT serialised: per docs/2. Internals/07 §7.4, ArtifactID is
	// computed as the hash of the full file bytes (including the
	// header), so it cannot live inside the body. The field is set
	// at two places only:
	//   - manifestcodec.ComputeArtifactID, after writing the body
	//     and hashing the result;
	//   - core.loadManifest, from the id used to fetch the file.
	// On the wire (manifestcodec) the field is invisible; in the
	// index (sqlite) it is the primary key. See codec_test.go for
	// the "ArtifactID does not appear in JSON" invariant.
	ArtifactID ArtifactID

	Type      ManifestType
	Namespace string
	SessionID string
	CreatedAt time.Time

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
