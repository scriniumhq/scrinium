// Encrypted Put/Get on the two crypto axes (Encryption Model §5.4):
// ManifestCrypto (Sealed/Paranoid) encrypts the manifest body, while a
// crypto Pipeline stage (aes-gcm) encrypts the blob payload. Covered here:
// Put/Get round-trips under each ManifestCrypto, Locked-store rejection,
// the ADR-58 regression that encrypted blobs do NOT dedup (random IV →
// distinct ciphertext, each independently readable), and the §3.5
// invariant that Paranoid Walk never decrypts manifest bodies. Plain blob
// pipeline round-trips themselves live in pipeline_test.go.

package storesuite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/pipeline/stage/aesgcm"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// initEncryptedWithCrypto bootstraps an encrypted Store with the requested
// ManifestCrypto and reopens it with AutoUnlock so it is ready to Put. The
// same WithConfig is passed to Init and Open — otherwise OpenStore reports
// a config mismatch against the persisted system.config artifact.
func initEncryptedWithCrypto(t *testing.T, crypto config.ManifestCrypto) store.Store {
	t.Helper()
	cfg := config.StoreConfig{ManifestCrypto: crypto}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))
	return r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithConfig(cfg),
	)
}

// payloadReader returns an Artifact (with a non-empty Usr field, so the
// encrypted-manifest path exercises Usr) and the original bytes for
// round-trip comparison.
func payloadReader(s string) (a domain.Artifact, raw []byte) {
	raw = []byte(s)
	a = domain.Artifact{
		Payload: bytes.NewReader(raw),
		Usr:     json.RawMessage(`{"tag":"x"}`),
	}
	return
}

// TestPut_AcrossManifestCrypto_Succeeds: a Put succeeds and returns a
// non-empty ArtifactID on a Plain store and on encrypted stores in both
// Sealed and Paranoid manifest-crypto modes.
func TestPut_AcrossManifestCrypto_Succeeds(t *testing.T) {
	cases := []struct {
		name string
		init func(t *testing.T) store.Store
	}{
		{"plain", func(t *testing.T) store.Store { s, _ := storefx.InitWithRoot(t); return s }},
		{"sealed", func(t *testing.T) store.Store { return initEncryptedWithCrypto(t, config.ManifestCryptoSealed) }},
		{"paranoid", func(t *testing.T) store.Store { return initEncryptedWithCrypto(t, config.ManifestCryptoParanoid) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.init(t)
			a, _ := payloadReader(tc.name + " payload")
			id, err := s.Put(context.Background(), a)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if id == "" {
				t.Fatal("ArtifactID is empty")
			}
		})
	}
}

// TestPutGet_AcrossManifestCrypto_RoundTrip: content survives a Put→Get
// round-trip through the manifest-encryption codec in both Sealed and
// Paranoid modes.
func TestPutGet_AcrossManifestCrypto_RoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		crypto config.ManifestCrypto
	}{
		{"sealed", config.ManifestCryptoSealed},
		{"paranoid", config.ManifestCryptoParanoid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := initEncryptedWithCrypto(t, tc.crypto)
			a, raw := payloadReader(tc.name + " end-to-end")

			id, err := s.Put(context.Background(), a)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			rh, err := s.Get(context.Background(), id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer rh.Close()

			got, err := io.ReadAll(rh)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(got, raw) {
				t.Errorf("payload round-trip: got %q, want %q", got, raw)
			}
		})
	}
}

// TestEncrypted_LockedRejectsOperations: a Locked encrypted store refuses
// operations at checkOperational with ErrLocked — both a Put (no manifest
// written) and a Get of a previously-written manifest. The Get path stops
// at the state gate before reaching the manifest codec.
func TestEncrypted_LockedRejectsOperations(t *testing.T) {
	cfg := config.StoreConfig{ManifestCrypto: config.ManifestCryptoParanoid}

	cases := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"put while locked", func(t *testing.T) error {
			_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))
			locked := r.Open(t,
				store.WithPassphrase(storefx.StaticPP("pw")),
				store.WithConfig(cfg),
			)
			a, _ := payloadReader("payload")
			_, err := locked.Put(context.Background(), a)
			return err
		}},
		{"get while locked", func(t *testing.T) error {
			_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))
			unlocked := r.Open(t,
				store.WithPassphrase(storefx.StaticPP("pw")),
				store.WithAutoUnlock(),
				store.WithConfig(cfg),
			)
			a, _ := payloadReader("payload")
			id, err := unlocked.Put(context.Background(), a)
			if err != nil {
				t.Fatalf("setup Put: %v", err)
			}
			locked := r.Open(t,
				store.WithPassphrase(storefx.StaticPP("pw")),
				store.WithConfig(cfg),
			)
			_, err = locked.Get(context.Background(), id)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(t); !errors.Is(err, errs.ErrLocked) {
				t.Fatalf("%s: got %v, want ErrLocked", tc.name, err)
			}
		})
	}
}

// TestPut_EncryptedBlobsDoNotDedup is the ADR-58 regression. The store
// encrypts BLOBS via a crypto Pipeline stage (aes-gcm); that non-empty
// crypto-identity ("aes-gcm/") takes the encrypted dedup branch. With
// EncryptedDedup Disabled, N writes of the same plaintext produce N
// distinct ArtifactIDs AND N distinct blobs (random IV → distinct
// ciphertext), and crucially every manifest stays independently readable
// — the pre-ADR-58 behaviour collapsed them onto one blob whose IV matched
// only the first writer, so later Gets failed with ErrDecryptionFailed.
func TestPut_EncryptedBlobsDoNotDedup(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i)
	}
	aesFactory, err := aesgcm.New(dek)
	if err != nil {
		t.Fatalf("aesgcm.New: %v", err)
	}
	reg := pipeline.NewTransformerRegistry().Register("aes-gcm", aesFactory)

	cfg := config.StoreConfig{Pipeline: []string{"aes-gcm"}}
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	s := storefx.InitOn(t, drv,
		store.WithStoreIndex(idx),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)

	const samePayload = "encrypted no-dedup payload"
	ids := make([]domain.ArtifactID, 0, 3)
	for i := 0; i < 3; i++ {
		a, _ := payloadReader(samePayload)
		id, err := s.Put(context.Background(), a)
		if err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// (a) Three distinct ArtifactIDs — ciphertext non-determinism.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[i] == ids[j] {
				t.Fatalf("distinct ArtifactIDs expected, identical at %d/%d: %s", i, j, ids[i])
			}
		}
	}

	// (b) Three blobs on disk — encrypted blobs do NOT dedup under
	// Disabled. This is the assertion that read "1" before ADR-58.
	disk := storefx.OnDiskAt(drv.Root())
	if blobCount := disk.BlobCount(); blobCount != 3 {
		t.Errorf("Disabled: 3 Puts of same plaintext should yield 3 blobs, got %d", blobCount)
	}

	// (c) Every manifest is independently readable — the property the bug
	// violated. Each blob decrypts under its own IV.
	for i, id := range ids {
		rh, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get id[%d]: %v", i, err)
		}
		got, _ := io.ReadAll(rh)
		_ = rh.Close()
		if string(got) != samePayload {
			t.Errorf("id[%d] payload: got %q, want %q", i, got, samePayload)
		}
	}

	// (d) Deleting one leaves the others intact (independent blobs, no
	// shared ref_count to confuse).
	if err := s.Delete(context.Background(), ids[0]); err != nil {
		t.Fatalf("Delete[0]: %v", err)
	}
	rh, err := s.Get(context.Background(), ids[1])
	if err != nil {
		t.Fatalf("Get id[1] after Delete[0]: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if string(got) != samePayload {
		t.Errorf("survivor payload: got %q, want %q", got, samePayload)
	}
}

// alwaysFailingResolver errors on any GetKeys call — used to prove a code
// path does NOT consult the resolver.
type alwaysFailingResolver struct{}

func (alwaysFailingResolver) GetKeys(_ string) ([][]byte, error) {
	return nil, errors.New("alwaysFailingResolver: should not be called")
}
func (alwaysFailingResolver) ResolveWriteKey(pipeline.KeyContext) string { return "" }

// TestWalk_ParanoidStoreWalksWithoutDecryption verifies the §3.5
// invariant: in Paranoid mode, Namespace is encrypted inside the manifest
// file but stored in plaintext in StoreIndex, so Walk returns matches from
// the index alone, never reading or decrypting manifest bodies. Proven by
// reopening with a sabotaged resolver that errors on every GetKeys call;
// Walk must still succeed and return the expected row count. (The same idx
// is reused across reopen so a cold index does not trip the Orphan Scan —
// backlog §3.1.)
func TestWalk_ParanoidStoreWalksWithoutDecryption(t *testing.T) {
	cfg := config.StoreConfig{ManifestCrypto: config.ManifestCryptoParanoid}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))

	s1 := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithConfig(cfg),
	)
	const n = 5
	for i := 0; i < n; i++ {
		a, _ := payloadReader(fmt.Sprintf("Paranoid payload %d", i))
		if _, err := s1.Put(context.Background(), a); err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
	}

	s2 := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithKeyResolver(alwaysFailingResolver{}),
		store.WithConfig(cfg),
	)

	count := 0
	if err := s2.Walk(context.Background(), func(domain.Manifest) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("Walk on Paranoid Store with broken resolver: %v\n"+
			"Walk must NOT decrypt manifest bodies — namespace lookup is index-only", err)
	}
	if count != n {
		t.Errorf("Walk row count: got %d, want %d", count, n)
	}
}
