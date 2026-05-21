package descriptor

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// ChecksumLen is the length of a descriptor checksum in bytes.
// Fixed at SHA-256 output size — the algorithm cannot be made
// configurable: the descriptor is read before StoreConfig (and
// thus ContentHasher) is known. See §10.1.3.
const ChecksumLen = 32

// Checksum returns the SHA-256 hash of the canonical serialised
// form of d, identical to what Marshal would write to disk. Two
// descriptors with the same Validate-passing content always
// produce the same checksum: Marshal is deterministic.
//
// The checksum lives outside the on-disk descriptor body — it is
// kept in store_meta as a separate cache entry (§10.1.3). This
// function exists to compute it; persistence is the caller's
// concern.
func Checksum(d *Descriptor) ([]byte, error) {
	if d == nil {
		return nil, errors.New("descriptor.Checksum: nil descriptor")
	}
	data, err := Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("descriptor.Checksum: %w", err)
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}
