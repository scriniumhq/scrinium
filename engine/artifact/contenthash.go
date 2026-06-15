package artifact

import (
	"encoding/hex"
	"fmt"
	"hash"

	"scrinium.dev/domain"
)

// contenthash.go — parsing a ContentHash string into the pieces a verifier
// needs. Pure: the only dependency is the HashRegistry passed in, which is
// itself a dependency-free factory of hashers.
//
// CryptoIdentity (the dedup-key crypto component, ADR-58) is NOT defined
// here — it lives in domain.CryptoIdentityOf, dependency-free, and both
// artifact and store use it directly. There is no reason to wrap it.

// ParseContentHash decodes a bare-hex ContentHash (ADR-93) into the
// expected raw digest bytes and a fresh hash.Hash for the given algorithm
// — everything a caller needs to re-hash a blob's plaintext and compare
// against the recorded hash.
//
// The algorithm is the store's immutable ContentHasher, supplied by the
// caller. A malformed hex string or an unregistered algorithm errors.
func ParseContentHash(reg domain.HashRegistry, algo string, ch domain.ContentHash) (want []byte, hasher hash.Hash, err error) {
	want, err = hex.DecodeString(string(ch))
	if err != nil {
		return nil, nil, fmt.Errorf("artifact: decode ContentHash: %w", err)
	}
	hasher, err = reg.NewHasher(algo)
	if err != nil {
		return nil, nil, fmt.Errorf("artifact: hasher %q: %w", algo, err)
	}
	return want, hasher, nil
}
