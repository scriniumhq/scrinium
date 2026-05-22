package plugins

import (
	"sync"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/aead"
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
		aead.Wipe(r.dek)
		r.dek = nil
	}
}
