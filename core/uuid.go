package core

import (
	"crypto/rand"
	"fmt"
)

// generateStoreID produces a fresh UUID v4 string. We avoid the
// google/uuid dependency for one call site: a couple of dozen
// lines of stdlib do the same thing, and the engine has zero
// other places that need UUIDs.
//
// The format follows RFC 4122 §4.4 (random version 4, variant 1).
func generateStoreID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("core: generate StoreID: %w", err)
	}
	// Version 4: top nibble of byte 6 is 0100.
	b[6] = (b[6] & 0x0f) | 0x40
	// Variant 1 (RFC 4122): top two bits of byte 8 are 10.
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3],
		b[4], b[5],
		b[6], b[7],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15],
	), nil
}