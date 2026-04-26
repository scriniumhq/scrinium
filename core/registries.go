package core

import (
	"encoding/hex"
	"errors"
	"hash"
	"strings"
	"sync"

	"github.com/rkurbatov/scrinium/domain"
)

// transformerRegistry implements TransformerRegistry with an RWMutex
// so concurrent registration and reads stay safe. In production
// registration usually happens once (when wiring the stack), but
// the protection is cheaper than chasing flaky races in tests.
type transformerRegistry struct {
	mu        sync.RWMutex
	factories map[string]TransformerFactory
}

func (r *transformerRegistry) Get(id string) (TransformerFactory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[id]
	if !ok {
		return nil, ErrUnsupportedAlgorithm
	}
	return f, nil
}

func (r *transformerRegistry) Register(id string, f TransformerFactory) TransformerRegistry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[id] = f
	return r
}

// hashRegistry implements HashRegistry. Hash identifiers in the
// project use the "<algo>-<hex>" format (for example,
// "sha256-abc123...").
type hashRegistry struct {
	mu      sync.RWMutex
	hashers map[string]func() hash.Hash
}

func (r *hashRegistry) Parse(h string) (algo string, raw []byte, err error) {
	dash := strings.IndexByte(h, '-')
	if dash <= 0 || dash == len(h)-1 {
		return "", nil, errors.New("core: invalid hash format, expected '<algo>-<hex>'")
	}
	algo = h[:dash]
	hexPart := h[dash+1:]
	raw, err = hex.DecodeString(hexPart)
	if err != nil {
		return "", nil, errors.New("core: invalid hash hex part: " + err.Error())
	}
	return algo, raw, nil
}

func (r *hashRegistry) NewHasher(algo string) (hash.Hash, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.hashers[algo]
	if !ok {
		return nil, ErrUnsupportedAlgorithm
	}
	return fn(), nil
}

func (r *hashRegistry) Format(algo string, raw []byte) string {
	return algo + "-" + hex.EncodeToString(raw)
}

func (r *hashRegistry) Register(algo string, fn func() hash.Hash) domain.HashRegistry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hashers[algo] = fn
	return r
}

// staticKeyResolver implements KeyResolver with a single DEK. It
// returns one key for any KeyID; DefaultKeyID is the empty string.
// This is the default behaviour for typical scenarios.
type staticKeyResolver struct {
	dek []byte
}

func (r *staticKeyResolver) GetKeys(keyID string) ([][]byte, error) {
	// Return a defensive copy so the caller cannot zero out or
	// modify the resolver's internal key.
	cp := make([]byte, len(r.dek))
	copy(cp, r.dek)
	return [][]byte{cp}, nil
}

func (r *staticKeyResolver) DefaultKeyID() string {
	return ""
}
