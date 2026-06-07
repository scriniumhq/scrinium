package hashing

import (
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
	"sync"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// registry implements domain.HashRegistry with an RWMutex so
// concurrent registration and reads stay safe. In production
// registration usually happens once (when wiring the stack), but the
// protection is cheaper than chasing flaky races in tests.
type registry struct {
	mu      sync.RWMutex
	hashers map[string]func() hash.Hash
}

var _ domain.HashRegistry = (*registry)(nil)

// NewHashRegistry creates an empty hash-algorithm registry. The host
// application registers factories through Register.
func NewHashRegistry() domain.HashRegistry {
	return &registry{hashers: make(map[string]func() hash.Hash)}
}

// Parse splits an "<algo>-<hex>" identifier into its algorithm name
// and raw bytes.
func (r *registry) Parse(h string) (algo string, raw []byte, err error) {
	dash := strings.IndexByte(h, '-')
	if dash <= 0 || dash == len(h)-1 {
		return "", nil, errors.New("hashing: invalid hash format, expected '<algo>-<hex>'")
	}
	algo = h[:dash]
	hexPart := h[dash+1:]
	raw, err = hex.DecodeString(hexPart)
	if err != nil {
		return "", nil, errors.New("hashing: invalid hash hex part: " + err.Error())
	}
	return algo, raw, nil
}

// NewHasher creates a fresh hash.Hash for the given algorithm.
func (r *registry) NewHasher(algo string) (hash.Hash, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.hashers[algo]
	if !ok {
		return nil, errs.ErrUnsupportedAlgorithm
	}
	return fn(), nil
}

// Format builds an "<algo>-<hex>" identifier from an algorithm name
// and raw digest bytes.
func (r *registry) Format(algo string, raw []byte) string {
	return algo + "-" + hex.EncodeToString(raw)
}

// Register registers a hasher factory under an algorithm name and
// returns the registry for chaining.
func (r *registry) Register(algo string, fn func() hash.Hash) domain.HashRegistry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hashers[algo] = fn
	return r
}

// NamingKeyPublic is the public domain-separation constant used as the
// naming key (NK) for Plain/Sealed stores, so the handle is publicly
// reproducible and self-verifiable. Paranoid uses a secret naming key
// instead (deferred). Treat as a versioned, immutable constant: changing
// it re-identifies every artifact.
var NamingKeyPublic = []byte("scrinium/artifact-id/v1")

// Handle computes the floating ArtifactID = H(NK ‖ cd ‖ md ‖ nonce).
//
// cd and md are "<algo>-<hex>" digests sharing the store's hash algo;
// their raw bytes are fixed-length within a store, so the concatenation
// is unambiguous (nonce is a fixed 16 bytes in Unique mode, empty in
// Coalesced — the mode is an immutable store property). nk is the naming
// key: NamingKeyPublic in Plain/Sealed.
//
// Hashing the concatenation is mandatory: with an empty identity
// partition md is a constant token, so a raw cd‖md would expose cd. H
// keeps the output indistinguishable from random.
func Handle(reg domain.HashRegistry, algo string, nk []byte, cd domain.ContentHash, md string, nonce []byte) (domain.ArtifactID, error) {
	_, cdRaw, err := reg.Parse(string(cd))
	if err != nil {
		return "", fmt.Errorf("hashing: parse cd: %w", err)
	}
	_, mdRaw, err := reg.Parse(md)
	if err != nil {
		return "", fmt.Errorf("hashing: parse md: %w", err)
	}
	h, err := reg.NewHasher(algo)
	if err != nil {
		return "", err
	}
	h.Write(nk)
	h.Write(cdRaw)
	h.Write(mdRaw)
	h.Write(nonce)
	return domain.ArtifactID(reg.Format(algo, h.Sum(nil))), nil
}
