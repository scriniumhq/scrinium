package artifact

import (
	"encoding/hex"
	"fmt"
	"hash"

	"scrinium.dev/domain"
)

// ParseContentHash decodes a bare-hex ContentHash (ADR-93) into the
// expected raw digest bytes and a fresh hash.Hash for the given algorithm.
func ParseContentHash(reg domain.HashRegistry, algo string, ch domain.ContentHash) (rawDigest []byte, hasher hash.Hash, err error) {
	rawDigest, err = hex.DecodeString(string(ch))
	if err != nil {
		return nil, nil, fmt.Errorf("artifact: decode ContentHash: %w", err)
	}
	hasher, err = reg.NewHasher(algo)
	if err != nil {
		return nil, nil, fmt.Errorf("artifact: hasher %q: %w", algo, err)
	}
	return rawDigest, hasher, nil
}
