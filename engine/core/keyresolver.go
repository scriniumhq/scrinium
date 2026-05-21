package core

// keyresolver.go — encryption-key resolution contract: KeyResolver,
// its write-time KeyContext, and the default static resolver
// constructor. Split out of the former plugins.go grab-bag.

// KeyResolver is the plugin that resolves a DEK by its string
// KeyID. It allows a Store to support several DEKs simultaneously:
// multi-tenant stores, mixed recovered data, intermediate states
// during key rotation, crypto-shredding.
//
// On write the engine calls ResolveWriteKey(KeyContext) to choose
// the KeyID, passes it to the blob Encoder via EncodeContext, and
// writes it into the manifest header. On read the KeyID is read
// from the header, GetKeys returns a list of candidates, and the
// engine transparently iterates over them until one decrypts
// successfully or the list is exhausted.
type KeyResolver interface {
	GetKeys(keyID string) ([][]byte, error)

	// ResolveWriteKey returns the KeyID to encrypt a new artifact
	// under, given its write-time context. The default
	// StaticKeyResolver ignores ctx and returns "" (one store,
	// one DEK). A custom resolver may map ctx.Namespace to a KeyID
	// to implement key-per-namespace. The read path never calls
	// this — the KeyID always comes from the manifest header.
	ResolveWriteKey(ctx KeyContext) string
}

// KeyContext carries the write-time context the engine hands to
// ResolveWriteKey. Extended additively — new fields are added
// without changing the method signature. See ADR-58.
type KeyContext struct {
	// Namespace is the artifact's namespace at write time.
	Namespace string
}

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
