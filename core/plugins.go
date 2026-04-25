package core

import (
	"context"
	"errors"
	"hash"
	"io"
	"time"

	"github.com/rkurbatov/scrinium/event"
)

// Publisher is the minimal contract for emitting events; it is
// passed to Store via WithPublisher. It is satisfied by
// event.EventBus and by any custom implementation (asynchronous,
// persistent, filtering).
type Publisher interface {
	Publish(e event.Event)
}

// --- Pipeline transformers ---

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
// Created via TransformerFactory.NewDecoder(stage); receives IV and
// other stage parameters via PipelineStage.
type Decoder interface {
	Transform(r io.Reader) io.Reader
}

// TransformResult is the result of an Encoder, captured by the
// Pipeline runner after EOF.
type TransformResult struct {
	// OutputSize — number of bytes that left the stage's output.
	OutputSize int64

	// IV — initialisation vector for crypto plugins. Written to
	// manifest.Pipeline[i].IV. nil for non-crypto plugins.
	IV []byte

	// Entropy — Shannon entropy of the output stream (for
	// compressors). Used to decide whether to skip compressing
	// uncompressible input.
	Entropy float64
}

// TransformerFactory is the factory of Encoder/Decoder instances
// for a single algorithm. State shared between instances (a common
// zstd dictionary, a common encryption key) belongs to the factory.
type TransformerFactory interface {
	NewEncoder() Encoder
	NewDecoder(stage PipelineStage) Decoder
}

// TransformerRegistry is the registry of transformation factories
// keyed by algorithm identifier (for example, "zstd", "aes-gcm").
// The identifier appears in the manifest in pipeline[].algorithm.
type TransformerRegistry interface {
	Get(id string) (TransformerFactory, error)
	Register(id string, f TransformerFactory) TransformerRegistry
}

// HashRegistry is the registry of hash algorithms. Used by the
// Pipeline runner for TeeReader at every stage, by the Recovery
// Agent when parsing TOC blobs and Pack TOCs, and by parsers of
// "<algo>-<hex>" identifiers.
type HashRegistry interface {
	// Parse splits an "<algo>-<hex>" identifier into the algorithm
	// name and the raw hash bytes.
	Parse(h string) (algo string, raw []byte, err error)

	// NewHasher creates a fresh hash.Hash for the given algorithm.
	NewHasher(algo string) (hash.Hash, error)

	// Format builds an identifier string from an algorithm name and
	// raw bytes.
	Format(algo string, raw []byte) string

	// Register registers a hasher factory under an algorithm name.
	// Returns the registry itself for chained registration.
	Register(algo string, fn func() hash.Hash) HashRegistry
}

// --- Encryption-key resolution ---

// KeyResolver is the plugin that resolves a DEK by its string
// KeyID. It allows a Store to support several DEKs simultaneously:
// multi-tenant stores, mixed recovered data, intermediate states
// during key rotation, crypto-shredding.
//
// On write the engine takes the KeyID from DefaultKeyID() and
// writes it into the manifest header. On read the KeyID is read
// from the header, GetKeys returns a list of candidates, and the
// engine transparently iterates over them until one decrypts
// successfully or the list is exhausted.
type KeyResolver interface {
	GetKeys(keyID string) ([][]byte, error)
	DefaultKeyID() string
}

// --- Maintenance agents ---

// MaintenanceAgent is the contract of a one-shot administrative
// operation. Declared here (rather than in agent/) so that Store
// can require a MaintenanceAgent to be validated through Validate
// without depending on higher layers.
type MaintenanceAgent interface {
	// Validate checks whether the operation is applicable to the
	// current state of the Store: required maintenance mode,
	// presence of required parameters, availability of
	// dependencies.
	Validate(ctx context.Context) error

	// Run starts the operation. It acquires a maintenance/lease,
	// performs the work, and releases the lease. It returns the
	// result with accumulated statistics.
	Run(ctx context.Context) (*AgentResult, error)
}

// AgentResult is the result of an agent's work (one-shot or one
// background cycle). Used in EventAgentCompleted and
// EventAgentCycle.
type AgentResult struct {
	AgentType   string
	StoreID     string
	StartedAt   time.Time
	CompletedAt time.Time
	Stats       map[string]int64
	Partial     bool // true if the work was interrupted and completed only partially
}

// --- Registry constructors ---

// NewTransformerRegistry creates an empty transformer registry.
// The host application registers factories through Register.
func NewTransformerRegistry() TransformerRegistry {
	return &transformerRegistry{factories: make(map[string]TransformerFactory)}
}

// NewHashRegistry creates an empty hash-algorithm registry.
// The host application registers factories through Register.
func NewHashRegistry() HashRegistry {
	return &hashRegistry{hashers: make(map[string]func() hash.Hash)}
}

// NewStaticKeyResolver creates a KeyResolver that returns the same
// DEK for any request. DefaultKeyID returns an empty string. This
// is the default behaviour: one Store, one DEK.
func NewStaticKeyResolver(dek []byte) KeyResolver {
	// Defensive copy so external code cannot modify the key after
	// passing it to the resolver.
	cp := make([]byte, len(dek))
	copy(cp, dek)
	return &staticKeyResolver{dek: cp}
}

// --- Registry sentinel errors ---

// ErrUnsupportedAlgorithm — the algorithm has not been registered.
// Returned by TransformerRegistry.Get and HashRegistry.NewHasher.
var ErrUnsupportedAlgorithm = errors.New("core: unsupported algorithm")
