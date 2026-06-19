package storesuite

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// TestPut_Sealed_UsrMetadataConfidential asserts the Sealed
// confidentiality guarantee on disk: after a Put whose Usr carries a
// marker, that marker must not survive in plaintext in the manifest
// file. Sealed encrypts the user-metadata section of the manifest body
// (system fields stay readable — that split is what separates Sealed
// from Paranoid).
//
// Black-box throughout. The manifest is read straight off the localfs
// root via storefx.OnDisk, never through an internal driver helper, so
// the test belongs in the suite rather than the package-internal set.
func TestPut_Sealed_UsrMetadataConfidential(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoSealed}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))
	s := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithConfig(cfg),
	)

	a, _ := payloadReader("payload")
	a.Usr = json.RawMessage(`{"secret":"do-not-leak"}`)
	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Raw manifest bytes off disk: the marker must not be present.
	path := storefx.OnDiskAt(r.Root()).ManifestPath(storekit.MustDigest(t, s, id))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest from disk: %v", err)
	}
	if bytes.Contains(raw, []byte("do-not-leak")) {
		t.Error("Sealed leaked usr metadata to plaintext")
	}
}
