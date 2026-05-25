package domain

import "hash"

// HashRegistry is the registry of hash algorithms. Used by the
// Pipeline runner for TeeReader at every stage, by the Recovery
// Agent when parsing TOC blobs and Pack TOCs, and by parsers of
// "<algo>-<hex>" identifiers.
//
// Lives in domain so that helpers (manifestcodec, future codecs,
// maintenance agents) can depend on the contract without pulling
// in store. The default implementation lives in core, constructed
// via store.NewHashRegistry().
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
