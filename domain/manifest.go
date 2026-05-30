package domain

import (
	"encoding/json"
	"io"
	"time"
)

// Artifact is the abstraction at the system boundary (input/output).
// It consists of a byte stream (Payload) and two metadata blocks
// per ADR-54:
//
//   - Ext: Scrinium-extension data the engine itself reads
//     (fsmeta and friends).
//   - Usr: opaque host-application data — tags, business
//     attributes; the engine never inspects them.
//
// Each block has a 64 KiB limit (MaxExtSize, MaxUsrSize).
type Artifact struct {
	Payload io.Reader

	Ext json.RawMessage
	Usr json.RawMessage
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

// Layout* are the canonical values for LayoutHeader.BlobStorage.
// Distinct from BlobStorage* (which is the StoreConfig-time policy):
// the configuration says "Inline", but the resolved layout
// for any specific manifest is either LayoutInline (the payload fit
// the inline limit) or LayoutTarget (it overflowed).
//
// Production code must compare against these constants, not the
// equivalent string literals.
const (
	LayoutInline = "Inline"
	LayoutTarget = "Target"
)

// PipelineStage is a single transformation stage in the Pipeline.
//
// KeyID is populated for crypto stages whose plugin resolves its
// DEK through a store.KeyResolver. On Put the Encoder records the
// KeyID the engine resolved (ResolveWriteKey) and passed via
// EncodeContext; on Get the Decoder looks up candidate keys for
// the recorded KeyID. The field is empty for non-crypto stages
// and for crypto plugins that pin the DEK at factory construction
// time (legacy single-key wiring).
type PipelineStage struct {
	Algorithm string
	Hash      string
	IV        []byte
	KeyID     string
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
	//   - store.loadManifest, from the id used to fetch the file.
	// On the wire (manifestcodec) the field is invisible; in the
	// index (sqlite) it is the primary key. See codec_test.go for
	// the "ArtifactID does not appear in JSON" invariant.
	ArtifactID ArtifactID

	Type      ManifestType
	Namespace string
	SessionID SessionID
	CreatedAt time.Time

	ContentHash  ContentHash
	OriginalSize int64
	BlobRef      BlobRef
	LayoutHeader LayoutHeader
	Pipeline     []PipelineStage
	InlineBlob   []byte

	RetentionUntil time.Time
	KeyID          string

	SystemFlags ManifestSystemFlags

	Ext json.RawMessage
	Usr json.RawMessage
}
