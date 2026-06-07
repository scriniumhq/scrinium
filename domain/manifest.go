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
	// ArtifactID is the floating external identity (handle):
	// PRF(NK, cd‖md). It is what the outside world holds and what
	// Put returns. Unlike the old model, it is SERIALISED in the
	// body — it is an input computed from cd‖md (+ nonce in Unique
	// mode), not the hash of the file, so it must be stored to be
	// reproducible and to survive index loss (rebuild reads it from
	// the manifest). Stable across form changes; changes only on
	// content (cd) or naming-key-domain change.
	ArtifactID ArtifactID

	// Digest is the manifest digest — hash of the full serialised
	// file bytes (header included). In-memory ONLY: it is the hash
	// of the body, so it cannot live inside the body. It is the
	// on-disk filename and the form-verifier; it CHANGES on repack.
	// Set at two places only:
	//   - artifact.ComputeManifestDigest, after encoding;
	//   - store.loadManifest, from the path used to fetch the file.
	// The index maps ArtifactID (handle) → Digest.
	Digest ManifestDigest

	// IdentityMetaHash is md = H(canon(identity-meta)), an input to
	// ArtifactID. SERIALISED. The identity partition is empty by
	// default → md is a constant token; an application may opt
	// specific fields into identity. Descriptive metadata
	// (CreatedAt, Namespace, Usr) and per-run fields are never in md.
	IdentityMetaHash string

	// IdentityNonce is 16 random bytes mixed into ArtifactID in
	// IdentityMode=Unique (makes the handle unique per Put); absent
	// in Coalesced. SERIALISED, so the handle stays reproducible.
	IdentityNonce []byte

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
