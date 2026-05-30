package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"os"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/internal/testutil/storefx"
)

// Store invariants — the laws that hold for ALL inputs. These subsume
// the bulk of the example-based Put/Get tests: round-trip,
// content-addressing, dedup, ref-count, and reopen stability are all
// consequences of the properties below.
//
// Each law's body lives in a check* helper and is driven by a
// Test*_Seeded function: the same law over a broad, deterministic
// spread of inputs (fixed RNG seed) on every `make test`. These are NOT
// fuzz targets. The payload is opaque to the engine — it stores bytes,
// it does not parse them — so byte-level mutation finds nothing a sized
// seeded loop does not, and the meaningful axis is size, not content
// (see TESTING.md, category 1). Fuzzing is reserved for code that
// parses untrusted bytes (the decoders) and for the operation-program
// state exploration in store_model_test.go.
//
// Plain store only: ArtifactID determinism and dedup are Plain-mode
// laws (Sealed/Paranoid use a random IV, so identical plaintext yields
// distinct ciphertext and distinct blobs by design). Encrypted
// behaviour is pinned by the bounded matrix test at the bottom of this
// file.

// mkArtifact wraps bytes as a domain.Artifact body.
func mkArtifact(b []byte) domain.Artifact {
	return domain.Artifact{Payload: bytes.NewReader(b)}
}

// getBytes Gets id and returns the full payload, failing the test on
// any error. The handle is always closed.
func getBytes(t *testing.T, s store.Store, id domain.ArtifactID) []byte {
	t.Helper()
	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll(%s): %v", id, err)
	}
	return got
}

// walkIDs returns the set of ArtifactIDs the store reports for ns.
func walkIDs(t *testing.T, s store.Store, ns string) map[domain.ArtifactID]struct{} {
	t.Helper()
	out := make(map[domain.ArtifactID]struct{})
	err := s.Walk(context.Background(), ns, func(m domain.Manifest) error {
		out[m.ArtifactID] = struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk(%q): %v", ns, err)
	}
	return out
}

// randBytes returns n pseudo-random bytes from rng. Shared by the
// seeded property drivers below.
func randBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	rng.Read(b)
	return b
}

// checkRoundTrip is the body of the round-trip law, shared by the fuzz
// target and the seeded driver: for any payload, Get(Put(x)) == x.
func checkRoundTrip(t *testing.T, payload []byte) {
	t.Helper()
	s := storefx.Init(t)
	id, err := s.Put(context.Background(), mkArtifact(payload), store.WithNamespace("u"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := getBytes(t, s, id); !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestStore_RoundTrip_Seeded drives the round-trip law over a broad,
// deterministic spread of sizes on every `make test`. Sizes span the
// inline/target boundary. (Not a fuzz target: the payload is opaque to
// the engine, so byte-level mutation adds nothing over a sized loop —
// see TESTING.md, category 1.)
func TestStore_RoundTrip_Seeded(t *testing.T) {
	rng := rand.New(rand.NewSource(2026))
	for i := 0; i < 200; i++ {
		checkRoundTrip(t, randBytes(rng, rng.Intn(1<<17)))
	}
}

// checkContentAddressing is the body of the content-addressing law:
// identical payload bytes deduplicate to a single on-disk blob, and
// distinct payloads occupy distinct blobs and yield distinct
// ArtifactIDs. Plain mode only (encrypted blobs use a random IV, so
// identical plaintext produces distinct ciphertext by design).
//
// IMPORTANT: ArtifactID is NOT a pure function of content. It hashes
// the manifest, which carries a second-resolution CreatedAt (plus
// session, namespace, ext). Two Puts of identical bytes therefore need
// not share an ID — under load (notably `-race`) they can land in
// different wall-clock seconds and get distinct IDs by design. So this
// law makes NO equal-content ID claim; the content-addressed invariant
// lives at the blob layer, which is what we assert.
func checkContentAddressing(t *testing.T, a, b []byte) {
	t.Helper()
	s, root := storefx.InitWithRoot(t)
	ctx := context.Background()

	id1, err := s.Put(ctx, mkArtifact(a), store.WithNamespace("u"))
	if err != nil {
		t.Fatalf("Put a #1: %v", err)
	}
	// A second Put of the same bytes must not create a second blob,
	// whether it reuses the manifest (same second) or writes a new one
	// (the blob is still deduped on content hash).
	if _, err := s.Put(ctx, mkArtifact(a), store.WithNamespace("u")); err != nil {
		t.Fatalf("Put a #2: %v", err)
	}

	id2, err := s.Put(ctx, mkArtifact(b), store.WithNamespace("u"))
	if err != nil {
		t.Fatalf("Put b: %v", err)
	}
	// Distinct content must not collide on ID (the content hash feeds
	// the manifest). Equal content makes no ID claim — see above.
	if !bytes.Equal(a, b) && id1 == id2 {
		t.Fatalf("distinct content collided on ID %s", id1)
	}

	// Content-addressing proper: identical payloads share one blob,
	// distinct payloads occupy distinct blobs.
	wantBlobs := 1
	if !bytes.Equal(a, b) {
		wantBlobs = 2
	}
	if n := storefx.OnDiskAt(root).BlobCount(); n != wantBlobs {
		t.Fatalf("blob count: got %d, want %d (a==b: %v)", n, wantBlobs, bytes.Equal(a, b))
	}

	// Both artifacts still read back correctly.
	if got := getBytes(t, s, id1); !bytes.Equal(got, a) {
		t.Fatalf("id1 read-back mismatch")
	}
	if got := getBytes(t, s, id2); !bytes.Equal(got, b) {
		t.Fatalf("id2 read-back mismatch")
	}
}

// TestStore_ContentAddressing_Seeded exercises both the equal and the
// distinct branch on every `make test`: roughly half the iterations
// reuse the same bytes for b (dedup path), the rest use fresh bytes.
func TestStore_ContentAddressing_Seeded(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for i := 0; i < 150; i++ {
		a := randBytes(rng, rng.Intn(4096))
		var b []byte
		if rng.Intn(2) == 0 {
			b = append([]byte(nil), a...) // equal path
		} else {
			b = randBytes(rng, rng.Intn(4096)) // (may coincide with a; check handles it)
		}
		checkContentAddressing(t, a, b)
	}
}

// checkReopenStable is the body of the reopen-stability law: closing
// and reopening preserves every artifact's content and the Walk set.
func checkReopenStable(t *testing.T, payload []byte) {
	t.Helper()
	s, r := storefx.InitPlain(t)
	ctx := context.Background()

	id, err := s.Put(ctx, mkArtifact(payload), store.WithNamespace("u"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	before := getBytes(t, s, id)
	beforeSet := walkIDs(t, s, "*")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := r.Open(t)
	if got := getBytes(t, s2, id); !bytes.Equal(got, before) {
		t.Fatalf("content changed across reopen")
	}
	afterSet := walkIDs(t, s2, "*")
	if len(afterSet) != len(beforeSet) {
		t.Fatalf("Walk set changed across reopen: %d -> %d", len(beforeSet), len(afterSet))
	}
	for id := range beforeSet {
		if _, ok := afterSet[id]; !ok {
			t.Fatalf("artifact %s disappeared across reopen", id)
		}
	}
}

// TestStore_ReopenStable_Seeded runs the reopen law over a broad spread
// of sizes on every `make test`.
func TestStore_ReopenStable_Seeded(t *testing.T) {
	rng := rand.New(rand.NewSource(4242))
	for i := 0; i < 128; i++ {
		checkReopenStable(t, randBytes(rng, rng.Intn(1<<17)))
	}
}

// --- Encrypted manifest confidentiality (bounded; KDF cost rules out fuzzing) ---

// TestStore_EncryptedManifestConfidentiality pins what ManifestCrypto
// actually guarantees — manifest-FIELD confidentiality — across modes:
//   - round-trip works with the right passphrase;
//   - the usr metadata never appears in the on-disk manifest bytes;
//   - Sealed leaves Namespace in plaintext, Paranoid hides it too;
//   - opening with the wrong passphrase fails closed.
//
// Note the scope: ManifestCrypto encrypts the MANIFEST, not the blob
// payload. Blob-at-rest encryption is a separate axis (StoreConfig.
// Pipeline = ["aes-gcm"] plus a KeyResolver) and is covered separately;
// asserting on blob bytes here would be testing the wrong mechanism.
func TestStore_EncryptedManifestConfidentiality(t *testing.T) {
	const usrSecret = "do-not-leak-usr-value"
	const ns = "tenant-secret-namespace"

	cases := []struct {
		mode     domain.ManifestCrypto
		nsHidden bool // Paranoid hides Namespace; Sealed leaves it visible
	}{
		{domain.ManifestCryptoSealed, false},
		{domain.ManifestCryptoParanoid, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.mode), func(t *testing.T) {
			cfg := domain.StoreConfig{ManifestCrypto: tc.mode}
			_, r := storefx.InitEncrypted(t, "correct-horse", store.WithConfig(cfg))
			s := r.Open(t,
				store.WithPassphrase(storefx.StaticPP("correct-horse")),
				store.WithAutoUnlock(),
				store.WithConfig(cfg),
			)
			ctx := context.Background()

			payload := []byte("encrypted-manifest payload")
			art := domain.Artifact{
				Payload: bytes.NewReader(payload),
				Usr:     json.RawMessage(`{"k":"` + usrSecret + `"}`),
			}
			id, err := s.Put(ctx, art, store.WithNamespace(ns))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}

			// Round-trip with the right key.
			if got := getBytes(t, s, id); !bytes.Equal(got, payload) {
				t.Fatalf("encrypted round-trip mismatch")
			}

			// Inspect the raw manifest file on disk.
			mp := storefx.OnDiskAt(r.Root()).ManifestPath(id)
			if mp == "" {
				t.Fatalf("ManifestPath returned empty for %s", id)
			}
			raw, err := os.ReadFile(mp)
			if err != nil {
				t.Fatalf("read manifest %s: %v", mp, err)
			}

			if bytes.Contains(raw, []byte(usrSecret)) {
				t.Errorf("%s: usr metadata leaked into manifest plaintext", tc.mode)
			}
			nsPresent := bytes.Contains(raw, []byte(ns))
			if tc.nsHidden && nsPresent {
				t.Errorf("Paranoid: Namespace leaked into manifest plaintext")
			}
			if !tc.nsHidden && !nsPresent {
				t.Errorf("Sealed: Namespace should remain in plaintext for index-free Walk")
			}
			_ = s.Close()

			// Wrong passphrase must fail closed.
			_, err = r.TryOpen(t,
				store.WithPassphrase(storefx.StaticPP("wrong")),
				store.WithAutoUnlock(),
				store.WithConfig(cfg),
			)
			if err == nil {
				t.Fatalf("open with wrong passphrase succeeded")
			}
			if !errors.Is(err, errs.ErrDecryptionFailed) {
				t.Logf("wrong-passphrase error (fail-closed, non-standard sentinel): %v", err)
			}
		})
	}
}
