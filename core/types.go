package core

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// Domain identifiers. All are typed strings to prevent accidental
// mixing.

// ArtifactID is the public identifier of an Artifact. It is a
// cryptographic hash of the final serialised manifest file (header
// included). Format: "<algo>-<hex>" (for example,
// "sha256-abc..."). Any change to the metadata produces a new
// Manifest and a new ArtifactID.
type ArtifactID string

// ContentHash is the hash of the original payload before any
// transformation. The global deduplication key: two files with the
// same content share a ContentHash regardless of Pipeline
// configuration.
type ContentHash string

// BlobRef is the hash of the final transformed blob stream (after
// compression and encryption). Used as the physical filename when
// blobs are stored individually. Applies to all blobs, including
// chunks and TOC blobs.
type BlobRef string

// StoreID is the global identifier of a Store. A UUID v4, generated
// once at InitStore; never changes.
type StoreID string

// ContentHashAlgorithm identifies a content-hashing algorithm.
// An immutable Store parameter: changing it breaks deduplication
// and verification of historical artifacts.
type ContentHashAlgorithm string

const (
	HashSHA256 ContentHashAlgorithm = "sha256"
	HashBLAKE3 ContentHashAlgorithm = "blake3"
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
	// ManifestTypeBlob — a regular Manifest that addresses a single
	// Blob.
	ManifestTypeBlob ManifestType = "blob"

	// ManifestTypeTOC — a Manifest of a chunked artifact; addresses
	// a TOC blob through which the stream is reassembled from chunks.
	ManifestTypeTOC ManifestType = "toc"

	// ManifestTypePack — an internal Manifest of a .pack volume;
	// invisible through Walk and non-existent from a client's
	// perspective.
	ManifestTypePack ManifestType = "pack"
)

// LayoutHeader is a service attribute inside a Manifest that
// "freezes" the physical-projection rules applied at write time.
// The read path ignores the current StoreConfig and relies on this
// header.
type LayoutHeader struct {
	BlobStorage string
}

// PipelineStage is a single transformation stage in the Pipeline.
// On Put, IV is filled by Encoder.Result(); on Get, IV is passed
// to the Decoder via the same object.
type PipelineStage struct {
	Algorithm string
	Hash      string
	IV        []byte
}

// ManifestSystemFlags is the system block of a Manifest. Present
// only for type: "pack" — it carries the offset and size of the
// TOC inside the pack file.
type ManifestSystemFlags struct {
	TOCOffset int64
	TOCSize   int64
}

// Manifest is the logical passport of an Artifact. It is the
// in-memory representation of the manifest file on disk; the parser
// fills KeyID from the file's header rather than from its body
// (format: see docs/2. Internals/07 Manifest Format §7.1).
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

	RetentionUntil time.Time // zero value = no retention protection
	KeyID          string    // DEK identifier; empty string = default key.
	// On disk KeyID lives in the file header, not in the body. The
	// parser extracts it from the header and fills this field.

	SystemFlags ManifestSystemFlags
	Metadata    json.RawMessage
}

// ReadHandle is the read primitive returned by Get. It hides the
// physical source of the bytes (a single file, a HostStorage record,
// a range read from a .pack volume, or an inline blob). Support for
// ReadAt/ReadAtCtx is reported by SupportsRandomAccess; outside of
// those conditions the calls return ErrRandomAccessNotSupported.
type ReadHandle interface {
	io.ReadCloser
	io.ReaderAt

	// SupportsRandomAccess reports the static availability of
	// ReadAt/ReadAtCtx. It depends on the source's physics and the
	// composition of the Pipeline.
	SupportsRandomAccess() bool

	// ReadAtCtx is the same as ReadAt but takes an explicit
	// cancellation context. Used with network drivers, slow media,
	// and operations that require an external timeout.
	ReadAtCtx(ctx context.Context, p []byte, off int64) (n int, err error)

	// Manifest returns the parsed manifest of the artifact. Available
	// immediately after Get, before the first Read. It does not block
	// or perform I/O.
	Manifest() Manifest
}
