package descriptor

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// ChecksumLen is the descriptor checksum length: SHA-256 output. Not
// configurable — the descriptor is read before ContentHasher is known.
const ChecksumLen = 32

// Checksum returns the SHA-256 of the canonical serialised form of d
// (what Marshal writes). Deterministic, so equal descriptors yield
// equal checksums. Persistence is the caller's concern.
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
