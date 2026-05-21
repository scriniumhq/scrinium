package plugins

import (
	"encoding/hex"
	"errors"
	"hash"
	"strings"
	"sync"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/manifestcrypto"
)

// transformerRegistry implements TransformerRegistry with an RWMutex
// so concurrent registration and reads stay safe. In production
// registration usually happens once (when wiring the stack), but
// the protection is cheaper than chasing flaky races in tests.
type transformerRegistry struct {
	mu        sync.RWMutex
	factories map[string]coreapi.TransformerFactory
}

func (r *transformerRegistry) Get(id string) (coreapi.TransformerFactory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[id]
	if !ok {
		return nil, errs.ErrUnsupportedAlgorithm
	}
	return f, nil
}

func (r *transformerRegistry) Register(id string, f coreapi.TransformerFactory) coreapi.TransformerRegistry {
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
		return "", nil, errors.New("plugins: invalid hash format, expected '<algo>-<hex>'")
	}
	algo = h[:dash]
	hexPart := h[dash+1:]
	raw, err = hex.DecodeString(hexPart)
	if err != nil {
		return "", nil, errors.New("plugins: invalid hash hex part: " + err.Error())
	}
	return algo, raw, nil
}

func (r *hashRegistry) NewHasher(algo string) (hash.Hash, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.hashers[algo]
	if !ok {
		return nil, errs.ErrUnsupportedAlgorithm
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
// returns one key for any KeyID; ResolveWriteKey ignores its
// context and returns an empty KeyID. This is the default
// behaviour for typical scenarios.
//
// mu guards dek so Close (called from store.Close) and GetKeys
// (called by manifestcodec on every encrypted decode) cannot race.
type staticKeyResolver struct {
	mu  sync.Mutex
	dek []byte
}

func (r *staticKeyResolver) GetKeys(keyID string) ([][]byte, error) {
	// Return a defensive copy so the caller cannot zero out or
	// modify the resolver's internal key.
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dek == nil {
		// Closed. The most accurate signal is "no keys for this
		// id"; the codec turns that into ErrKeyNotFound, which is
		// what a caller of a closed Store should see.
		return nil, nil
	}
	cp := make([]byte, len(r.dek))
	copy(cp, r.dek)
	return [][]byte{cp}, nil
}

func (r *staticKeyResolver) ResolveWriteKey(coreapi.KeyContext) string {
	return ""
}

// Close wipes the resolver's internal DEK copy. Called by
// store.Close via an anonymous-interface assertion. Idempotent.
func (r *staticKeyResolver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dek != nil {
		manifestcrypto.Wipe(r.dek)
		r.dek = nil
	}
}
