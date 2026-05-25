package domain

import (
	"encoding/json"
	"fmt"
)

// UnmarshalJSON accepts both the current ManifestCrypto values
// ("Plain", "Sealed", "Paranoid") and the pre-ADR-55 names
// ("Sealed", "Paranoid"). Old system.config artifacts
// written before the rename remain readable; newly-serialised
// configs always use the current names.
//
// The bridge lives on the value type rather than at the codec
// layer because StoreConfig is also marshalled through stock
// encoding/json in writeSystemConfig — a Custom Unmarshaller
// keeps the migration transparent to every caller.
//
// Sealed is mapped to Sealed; Paranoid is mapped to
// Paranoid. The mapping is one-way (read only) — writes always
// use the new names.
func (c *ManifestCrypto) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch s {
	case "":
		*c = ""
	case "Plain":
		*c = ManifestCryptoPlain
	case "Sealed":
		*c = ManifestCryptoSealed
	case "Paranoid":
		*c = ManifestCryptoParanoid
	default:
		return fmt.Errorf("domain.ManifestCrypto: unknown value %q", s)
	}
	return nil
}
