package store

import (
	"fmt"
	"hash"

	"scrinium.dev/engine/domain"
)

// parseContentHash splits an artifact's ContentHash identifier
// into a fresh hasher and the expected raw digest. Used by every
// integrity-check path: Verify, the VerifyOnRead wrapper, and
// (M3) the Scrub Agent.
//
// The algorithm is taken from the ContentHash prefix rather than
// the current StoreConfig so artifacts written under a previous
// hasher still validate after a ContentHasher migration. Callers
// must stream the plaintext bytes through `hasher` and compare
// hasher.Sum(nil) to `want` with bytes.Equal.
//
// Returned errors are wrapped with a stable prefix so callers
// can format their own context without losing the cause:
//
//	algo, want, hasher, err := s.parseContentHash(m.ContentHash)
//	if err != nil {
//	    return fmt.Errorf("core.Verify: %w", err)
//	}
//
// `algo` is returned alongside the hasher so callers that need it
// for diagnostics (mismatch messages) avoid a second Parse.
func (s *store) parseContentHash(ch domain.ContentHash) (algo string, want []byte, hasher hash.Hash, err error) {
	algo, want, err = s.hashes.Parse(string(ch))
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse ContentHash: %w", err)
	}
	hasher, err = s.hashes.NewHasher(algo)
	if err != nil {
		return "", nil, nil, fmt.Errorf("hasher %q: %w", algo, err)
	}
	return algo, want, hasher, nil
}
