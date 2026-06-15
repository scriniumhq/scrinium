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
//   - Ext: Scrinium-custom index data the engine itself reads
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

// Manifest is the logical passport of an Artifact.
//
// Reference model (ADR-92): a manifest carries an identity slot
// (handle / name / empty) plus two ordered reference arrays —
// BlobRefs (blobs: physical content) and HandleRefs (edges to other
// artifacts: the content-addressed DAG). Direction is implied by the
// slot: a filled slot consumes its members (top-down, +ref_count); an
// empty slot (pack container) places them (bottom-up, no ref_count).
//
// MIGRATION NOTE: the Namespace field is still transitional
// (ADR-79), serialised until its readers migrate. The legacy single
// BlobRef has been removed (ADR-92) — the россыпь blob is BlobRefs[0].
type Manifest struct {
	// ArtifactID is the floating external identity (handle):
	// PRF(NK, cd‖md). It is what the outside world holds and what
	// Put returns. SERIALISED in the body — it is an input computed
	// from cd‖md (+ nonce in Unique mode), not the hash of the file,
	// so it must be stored to be reproducible and to survive index
	// loss (rebuild reads it from the manifest). Stable across form
	// changes; changes only on content (cd) or naming-key-domain
	// change. Empty for system artifacts (slot filled by Name) and
	// pack containers (slot empty).
	ArtifactID ArtifactID

	// Name is the identity slot of a system artifact (config/<seq>,
	// scrub/cursor, migration/pending, …), written via
	// AdminStore.System().Put (ADR-85). Present ⟺ system artifact;
	// the user Put path never sets it. Empty for user artifacts and
	// pack containers.
	Name string

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
	// (CreatedAt, Usr) and per-run fields are never in md.
	IdentityMetaHash string

	// IdentityNonce is 16 random bytes mixed into ArtifactID in
	// IdentityMode=Unique (makes the handle unique per Put); absent
	// in Coalesced. SERIALISED, so the handle stays reproducible.
	IdentityNonce []byte

	// Namespace is the per-store organisational label (nsid).
	// Deprecated (ADR-79/93): the namespace is a CustomIndex + registry
	// and the nsid stamp is client-supplied data in Ext, not a core
	// field. Retained transitionally (still serialised) until the
	// namespace readers migrate to Ext.
	Namespace string

	SessionID SessionID
	CreatedAt time.Time

	ContentHash  ContentHash
	OriginalSize int64

	// HashAlgo is the content-address algorithm ("sha256"/"blake3"): the
	// store's immutable ContentHasher, recorded once (ADR-93). The
	// bare-hex content_hash / blob_refs and the manifest digest are
	// re-hashable from it without StoreConfig (self-description, Principle 3).
	HashAlgo string

	// BlobRefs is the ordered array of blob references the manifest
	// owns (ADR-92/93), at most 65535. Direction is implied by the
	// identity slot:
	//   - plain blob (россыпь): a single element;
	//   - composite (chunker, "composite" flag in Ext): the ordered
	//     list of chunk hashes — the source of truth, no TOC blob
	//     (ADR-87);
	//   - pack container (empty slot): [toc_blob, pack_blob] (ADR-86).
	// Empty for the Inline strategy (the inline blob is not tracked).
	BlobRefs []BlobRef

	// HandleRefs is the ordered array of edges to OTHER artifacts —
	// the content-addressed DAG (ADR-92), at most 65535. Elements are
	// HandleRef (the handle address space), symmetric to BlobRefs/BlobRef.
	// Direction by slot: a filled slot consumes the targets (+ref_count,
	// top-down); an empty slot (pack container) places them (bottom-up, no
	// ref_count — the pack members). nil/empty for a plain blob and a
	// composite (whose members are blobs, in BlobRefs).
	HandleRefs []HandleRef

	LayoutHeader LayoutHeader
	Pipeline     []PipelineStage
	InlineBlob   []byte

	RetentionUntil time.Time
	KeyID          string

	Ext json.RawMessage
	Usr json.RawMessage
}

// IsUser reports whether the manifest is a user artifact — the
// identity slot is filled by the floating handle (ADR-92). User
// artifacts are the roots of the DAG visible to Walk/Get.
func (m *Manifest) IsUser() bool { return m.ArtifactID != "" }

// IsSystem reports whether the manifest is a system artifact — the
// identity slot is filled by Name (ADR-85). System artifacts are
// addressed by name and excluded from the user Walk.
func (m *Manifest) IsSystem() bool { return m.Name != "" }

// IsContainer reports whether the manifest is a headless pack
// container — both identity slots are empty (ADR-86/92). A container
// is the rebuild anchor of a pack volume: it carries
// BlobRefs = [toc_blob, pack_blob] and HandleRefs = members (as
// placement) and is excluded from the user Walk.
func (m *Manifest) IsContainer() bool { return m.ArtifactID == "" && m.Name == "" }

// PrimaryBlobRef returns the россыпь blob reference (BlobRefs[0]) — the
// single physical blob a plain-blob manifest owns — or "" when the
// manifest has no blobs (Inline). Convenience for readers that handled
// a single BlobRef before ADR-92; the array stays the source of truth.
func (m Manifest) PrimaryBlobRef() BlobRef {
	if len(m.BlobRefs) > 0 {
		return m.BlobRefs[0]
	}
	return ""
}

// IsComposite reports whether the manifest is a chunked composite —
// the chunker's "composite" flag is set in Ext (ADR-87). The flag is
// for the chunker custom index; the core does not branch on it. For a
// composite, BlobRefs holds the ordered chunk list.
func (m *Manifest) IsComposite() bool {
	if len(m.Ext) == 0 {
		return false
	}
	var probe struct {
		Composite bool `json:"composite"`
	}
	if err := json.Unmarshal(m.Ext, &probe); err != nil {
		return false
	}
	return probe.Composite
}
