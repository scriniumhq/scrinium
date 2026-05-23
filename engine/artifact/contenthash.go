package artifact

import (
	"fmt"
	"hash"

	"scrinium.dev/engine/domain"
)

// contenthash.go — parsing a ContentHash string into the pieces a verifier
// needs. Pure: the only dependency is the HashRegistry passed in, which is
// itself a dependency-free factory of hashers.
//
// CryptoIdentity (the dedup-key crypto component, ADR-58) is NOT defined
// here — it lives in domain.CryptoIdentityOf, dependency-free, and both
// artifact and store use it directly. There is no reason to wrap it.

// ParseContentHash splits a ContentHash of the form "<algo>-<hex>" into
// its algorithm, the expected raw digest bytes, and a fresh hash.Hash for
// that algorithm — everything a caller needs to re-hash a blob's plaintext
// and compare against the recorded hash.
//
// The algorithm is taken from the ContentHash itself, never from the
// current StoreConfig, so an artifact written under a previous hasher
// still verifies. A malformed string or an unregistered algorithm errors.
func ParseContentHash(reg domain.HashRegistry, ch domain.ContentHash) (algo string, want []byte, hasher hash.Hash, err error) {
	algo, want, err = reg.Parse(string(ch))
	if err != nil {
		return "", nil, nil, fmt.Errorf("artifact: parse ContentHash: %w", err)
	}
	hasher, err = reg.NewHasher(algo)
	if err != nil {
		return "", nil, nil, fmt.Errorf("artifact: hasher %q: %w", algo, err)
	}
	return algo, want, hasher, nil
}
