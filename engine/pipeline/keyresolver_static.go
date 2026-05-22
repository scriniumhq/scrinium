package pipeline

import (
	"sync"

	"scrinium.dev/engine/internal/aead"
)

// NewStaticKeyResolver creates a KeyResolver that returns the same
// DEK for any request. ResolveWriteKey ignores its context and
// returns an empty KeyID. This is the default behaviour: one Store, one DEK.
func NewStaticKeyResolver(dek []byte) KeyResolver {
	// Defensive copy so external code cannot modify the key after
	// passing it to the resolver.
	cp := make([]byte, len(dek))
	copy(cp, dek)
	return &staticKeyResolver{dek: cp}
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

func (r *staticKeyResolver) ResolveWriteKey(KeyContext) string {
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
