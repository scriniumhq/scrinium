package plugins

import (
	"hash"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
)

// NewStaticKeyResolver creates a KeyResolver that returns the same
// DEK for any request. ResolveWriteKey ignores its context and
// returns an empty KeyID. This is the default behaviour: one Store, one DEK.
func NewStaticKeyResolver(dek []byte) coreapi.KeyResolver {
	// Defensive copy so external code cannot modify the key after
	// passing it to the resolver.
	cp := make([]byte, len(dek))
	copy(cp, dek)
	return &staticKeyResolver{dek: cp}
}

// NewTransformerRegistry creates an empty transformer registry.
// The host application registers factories through Register.
func NewTransformerRegistry() coreapi.TransformerRegistry {
	return &transformerRegistry{factories: make(map[string]coreapi.TransformerFactory)}
}

// NewHashRegistry creates an empty hash-algorithm registry.
// The host application registers factories through Register.
func NewHashRegistry() domain.HashRegistry {
	return &hashRegistry{hashers: make(map[string]func() hash.Hash)}
}
