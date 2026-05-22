package pipeline

import (
	"io"

	"scrinium.dev/engine/domain"
)

// transformer.go — Pipeline transformation contracts: the
// Encoder/Decoder pair, their factory, the per-write EncodeContext,
// the AEAD capability marker, and the algorithm registry. Split out
// of the former plugins.go grab-bag.

// Encoder is the per-write transformation plugin (used by Put).
// Created via TransformerFactory.NewEncoder(); lives for one
// operation. It is not required to be safe for concurrent use.
type Encoder interface {
	// Transform takes an incoming io.Reader and returns a wrapped
	// one. The Pipeline runner builds the chain: the output of one
	// stage is the input of the next. O(1) memory — no buffering of
	// the entire stream.
	Transform(r io.Reader) io.Reader

	// Result is called by the Pipeline runner after EOF — once the
	// whole stream has flowed through this Encoder. It returns the
	// transformation metrics.
	Result() TransformResult
}

// Decoder is the per-read transformation plugin (used by Get).
// Created via TransformerFactory.NewDecoder(stage); receives the
// stage parameters via PipelineStage.
type Decoder interface {
	Transform(r io.Reader) io.Reader
}

// TransformResult is the result of an Encoder, captured by the
// Pipeline runner after EOF.
type TransformResult struct {
	// OutputSize — number of bytes that left the stage's output.
	OutputSize int64

	// IV — initialisation vector for crypto plugins, written to
	// manifest.Pipeline[i].IV. nil for non-crypto plugins AND for
	// segmented-AEAD crypto plugins (ADR-59): a segmented blob keeps
	// one IV per segment inside the blob, so there is no single
	// per-blob IV to record. The Decoder reads the IV from each
	// segment frame, not from the stage.
	IV []byte

	// KeyID — the identifier of the DEK used to encrypt this
	// stage's output. Written to manifest.Pipeline[i].KeyID;
	// consulted on read by the Decoder to look up candidate keys
	// through the KeyResolver. Empty for non-crypto stages and
	// for crypto plugins constructed with a pinned DEK.
	KeyID string

	// Entropy — Shannon entropy of the output stream (for
	// compressors). Used to decide whether to skip compressing
	// uncompressible input.
	Entropy float64
}

// TransformerFactory is the factory of Encoder/Decoder instances
// for a single algorithm. State shared between instances (a common
// zstd dictionary, a common encryption key) belongs to the factory.
type TransformerFactory interface {
	NewEncoder(ctx EncodeContext) Encoder
	NewDecoder(stage domain.PipelineStage) Decoder
}

// EncodeContext carries the per-operation write context the engine
// hands to NewEncoder. Crypto factories read KeyID — chosen once by
// the engine via KeyResolver.ResolveWriteKey — to fetch the DEK
// through GetKeys, and read EncryptedDedup/SegmentSize to frame the
// blob (ADR-58/59). Non-crypto factories ignore every field.
// Extended additively. See ADR-58.
type EncodeContext struct {
	// KeyID is the resolved write-key id for this operation. Empty
	// for the default single-key resolver and for non-crypto
	// stages.
	KeyID string

	// EncryptedDedup is the store's deduplication policy for
	// encrypted blobs, mirrored from StoreConfig (ADR-58). A crypto
	// encoder maps it to the segment IV mode: Disabled (or empty) →
	// random per-segment IVs; Convergent → deterministic per-segment
	// IVs (ADR-59). Ignored by non-crypto stages.
	EncryptedDedup domain.EncryptedDedup

	// SegmentSize is the plaintext segment size for the segmented
	// AEAD blob format, mirrored from StoreConfig.SegmentSize
	// (immutable, ADR-59). Zero means "use the plugin default"
	// (≈1 MiB). Ignored by non-crypto stages.
	SegmentSize int
}

// AEADCapable is the anonymous capability interface a
// TransformerFactory implements when its Encoder/Decoder pair is
// an authenticated encryption with associated data (AEAD)
// primitive. The engine detects this via type assertion to skip
// redundant ContentHash recomputation on Get when the on-disk
// bytes are already covered by an AEAD tag — see the VerifyOnRead
// policy and docs/3. Reference/11 Configuration.
//
// The method is intentionally a marker: any non-trivial payload
// would lock the contract to a specific algorithm shape, while
// detection only needs a yes/no signal. Plugin implementations
// add an empty AEAD() method to opt in.
type AEADCapable interface {
	AEAD()
}

// TransformerRegistry is the registry of transformation factories
// keyed by algorithm identifier (for example, "zstd", "aes-gcm").
// The identifier appears in the manifest in pipeline[].algorithm.
type TransformerRegistry interface {
	Get(id string) (TransformerFactory, error)
	Register(id string, f TransformerFactory) TransformerRegistry
}
