// Store invariants — the laws that hold for ALL inputs, plus the blob
// deduplication matrix. The seeded laws (round-trip, content-addressing,
// reopen stability) subsume the bulk of the example-based Put/Get tests;
// TestDedup_Matrix is the single source of truth for the dedup decision
// across crypto configs (ADR-29 + ADR-58/59); TestStore_EncryptedManifestConfidentiality
// pins what ManifestCrypto guarantees.
//
// Each law's body lives in a check* helper driven by a Test*_Seeded function:
// the same law over a broad, deterministic spread of inputs (fixed RNG seed)
// on every `make test`. These are NOT fuzz targets — the payload is opaque to
// the engine, so byte-level mutation finds nothing a sized seeded loop does
// not (TESTING.md category 1). Fuzzing is reserved for the decoders and for
// the operation-program state exploration in model_test.go.
//
// Plain store only for the seeded dedup/ID laws: ArtifactID determinism and
// dedup are Plain-mode laws (Sealed/Paranoid use a random IV, so identical
// plaintext yields distinct ciphertext and distinct blobs by design).
// Encrypted behaviour is pinned by TestDedup_Matrix and the confidentiality
// test below.

package storesuite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"os"
	"strings"
	"testing"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/pipeline/stage/aesgcm"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// randBytes returns n pseudo-random bytes from rng. Shared by the seeded
// property drivers below.
func randBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	rng.Read(b)
	return b
}

// checkRoundTrip is the body of the round-trip law: for any payload,
// Get(Put(x)) == x.
func checkRoundTrip(t *testing.T, payload []byte) {
	t.Helper()
	s := storefx.Init(t)
	id, err := s.Put(context.Background(), artifactfx.PayloadBytes(payload))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := storekit.GetBytes(t, s, id); !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestStore_RoundTrip_Seeded drives the round-trip law over a broad,
// deterministic spread of sizes spanning the inline/target boundary.
func TestStore_RoundTrip_Seeded(t *testing.T) {
	rng := rand.New(rand.NewSource(2026))
	for i := 0; i < 200; i++ {
		checkRoundTrip(t, randBytes(rng, rng.Intn(1<<17)))
	}
}

// checkContentAddressing is the body of the content-addressing law:
// identical payload bytes deduplicate to a single on-disk blob, and distinct
// payloads occupy distinct blobs and yield distinct ArtifactIDs. Plain mode
// only (encrypted blobs use a random IV).
//
// IMPORTANT: ArtifactID is NOT a pure function of content. It hashes the
// manifest, which carries a second-resolution CreatedAt (plus session,
// namespace, ext). Two Puts of identical bytes therefore need not share an ID
// — under load (notably `-race`) they can land in different wall-clock seconds.
// So this law makes NO equal-content ID claim; the content-addressed invariant
// lives at the blob layer, which is what we assert.
func checkContentAddressing(t *testing.T, a, b []byte) {
	t.Helper()
	s, root := storefx.InitWithRoot(t)
	ctx := context.Background()

	id1, err := s.Put(ctx, artifactfx.PayloadBytes(a))
	if err != nil {
		t.Fatalf("Put a #1: %v", err)
	}
	// A second Put of the same bytes must not create a second blob, whether it
	// reuses the manifest (same second) or writes a new one (the blob is still
	// deduped on content hash).
	if _, err := s.Put(ctx, artifactfx.PayloadBytes(a)); err != nil {
		t.Fatalf("Put a #2: %v", err)
	}

	id2, err := s.Put(ctx, artifactfx.PayloadBytes(b))
	if err != nil {
		t.Fatalf("Put b: %v", err)
	}
	// Distinct content must not collide on ID. Equal content makes no ID claim.
	if !bytes.Equal(a, b) && id1 == id2 {
		t.Fatalf("distinct content collided on ID %s", id1)
	}

	// Content-addressing proper: identical payloads share one blob, distinct
	// payloads occupy distinct blobs.
	wantBlobs := 1
	if !bytes.Equal(a, b) {
		wantBlobs = 2
	}
	if n := storefx.OnDiskAt(root).BlobCount(); n != wantBlobs {
		t.Fatalf("blob count: got %d, want %d (a==b: %v)", n, wantBlobs, bytes.Equal(a, b))
	}

	// Both artifacts still read back correctly.
	if got := storekit.GetBytes(t, s, id1); !bytes.Equal(got, a) {
		t.Fatalf("id1 read-back mismatch")
	}
	if got := storekit.GetBytes(t, s, id2); !bytes.Equal(got, b) {
		t.Fatalf("id2 read-back mismatch")
	}
}

// TestStore_ContentAddressing_Seeded exercises both the equal and the distinct
// branch: roughly half the iterations reuse the same bytes for b (dedup path),
// the rest use fresh bytes.
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

// checkReopenStable is the body of the reopen-stability law: closing and
// reopening preserves every artifact's content and the Walk set.
func checkReopenStable(t *testing.T, payload []byte) {
	t.Helper()
	s, r := storefx.InitPlain(t)
	ctx := context.Background()

	id, err := s.Put(ctx, artifactfx.PayloadBytes(payload))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	before := storekit.GetBytes(t, s, id)
	beforeSet := storekit.WalkIDs(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := r.Open(t)
	if got := storekit.GetBytes(t, s2, id); !bytes.Equal(got, before) {
		t.Fatalf("content changed across reopen")
	}
	afterSet := storekit.WalkIDs(t, s2)
	if len(afterSet) != len(beforeSet) {
		t.Fatalf("Walk set changed across reopen: %d -> %d", len(beforeSet), len(afterSet))
	}
	for id := range beforeSet {
		if _, ok := afterSet[id]; !ok {
			t.Fatalf("artifact %s disappeared across reopen", id)
		}
	}
}

// TestStore_ReopenStable_Seeded runs the reopen law over a broad spread of sizes.
func TestStore_ReopenStable_Seeded(t *testing.T) {
	rng := rand.New(rand.NewSource(4242))
	for i := 0; i < 128; i++ {
		checkReopenStable(t, randBytes(rng, rng.Intn(1<<17)))
	}
}

// --- blob dedup matrix --------------------------------------------

// TestDedup_Matrix is the single source of truth for the blob deduplication
// decision at the Store layer (ADR-29 + ADR-58/59). Each row writes a sequence
// of payloads under one Store config and asserts (1) the number of physical
// blobs on disk matches the dedup policy, and (2) every artifact still reads
// back its exact payload — the no-data-loss invariant the M2 encrypted-dedup
// bug violated.
//
// The matrix covers everything observable at the Store layer with a single DEK
// (pinned aes-gcm). Two cross-config invariants (Plain vs encrypted never
// collapse; a different KeyID never collapses) live one layer down in the
// index conformance suite, since a single Store cannot host both at once.
func TestDedup_Matrix(t *testing.T) {
	// A payload large enough to span several 4 KiB segments, to prove the
	// per-segment Convergent IV derivation stays reproducible across segment
	// boundaries (ADR-59).
	multiSegment := strings.Repeat("scrinium-convergent-", 600) // ~12 KiB

	cases := []struct {
		name        string
		encrypted   bool // wire a pinned aes-gcm crypto stage
		dedup       config.EncryptedDedup
		segmentSize int
		payloads    []string
		wantBlobs   int
	}{
		{
			name:      "Plain/SameContent_dedups",
			payloads:  []string{"identical", "identical"},
			wantBlobs: 1, // ADR-29: (ContentHash, OriginalSize)
		},
		{
			name:      "Plain/DifferentContent_distinct",
			payloads:  []string{"alpha", "beta"},
			wantBlobs: 2,
		},
		{
			name:      "EncryptedDisabled/SameContent_neverDedups",
			encrypted: true,
			dedup:     config.EncryptedDedupDisabled,
			payloads:  []string{"secret", "secret", "secret"},
			wantBlobs: 3, // random IV per write → 3 distinct ciphertexts
		},
		{
			name:      "EncryptedDisabled/DifferentContent_distinct",
			encrypted: true,
			dedup:     config.EncryptedDedupDisabled,
			payloads:  []string{"secret-a", "secret-b"},
			wantBlobs: 2,
		},
		{
			name:      "EncryptedConvergent/SameContent_dedups",
			encrypted: true,
			dedup:     config.EncryptedDedupConvergent,
			payloads:  []string{"converge", "converge", "converge"},
			wantBlobs: 1, // deterministic IV → identical ciphertext → one BlobRef
		},
		{
			name:      "EncryptedConvergent/DifferentContent_distinct",
			encrypted: true,
			dedup:     config.EncryptedDedupConvergent,
			payloads:  []string{"converge-a", "converge-b"},
			wantBlobs: 2,
		},
		{
			name:        "EncryptedConvergent/MultiSegment_dedups",
			encrypted:   true,
			dedup:       config.EncryptedDedupConvergent,
			segmentSize: 4096,
			payloads:    []string{multiSegment, multiSegment},
			wantBlobs:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			drv := driverfx.LocalFS(t)

			cfg := config.StoreConfig{}
			opts := []store.StoreOption{store.WithStoreIndex(indexfx.Memory(t))}
			if tc.encrypted {
				dek := make([]byte, 32)
				for i := range dek {
					dek[i] = byte(i)
				}
				f, err := aesgcm.New(dek)
				if err != nil {
					t.Fatalf("aesgcm.New: %v", err)
				}
				cfg.Pipeline = []string{"aes-gcm"}
				cfg.EncryptedDedup = tc.dedup
				cfg.SegmentSize = tc.segmentSize
				opts = append(opts,
					store.WithReadRegistry(pipeline.NewTransformerRegistry().Register("aes-gcm", f)))
			}
			opts = append(opts, store.WithConfig(cfg))
			s := storefx.InitOn(t, drv, opts...)

			ids := make([]domain.ArtifactID, len(tc.payloads))
			for i, p := range tc.payloads {
				id, err := s.Put(ctx,
					domain.Artifact{Payload: bytes.NewReader([]byte(p))},
				)
				if err != nil {
					t.Fatalf("Put #%d: %v", i, err)
				}
				ids[i] = id
			}

			// (1) physical blob count matches the dedup policy.
			if got := storefx.OnDiskAt(drv.Root()).BlobCount(); got != tc.wantBlobs {
				t.Errorf("BlobCount = %d, want %d", got, tc.wantBlobs)
			}

			// (2) no-data-loss: every artifact reads back its payload, whether
			// it owns a blob or shares a deduplicated one.
			assertReadable(t, ctx, s, ids, tc.payloads)
		})
	}
}

func assertReadable(t *testing.T, ctx context.Context, s store.Store, ids []domain.ArtifactID, payloads []string) {
	t.Helper()
	for i, id := range ids {
		rh, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get id[%d]: %v", i, err)
		}
		got, err := io.ReadAll(rh)
		_ = rh.Close()
		if err != nil {
			t.Fatalf("ReadAll id[%d]: %v", i, err)
		}
		if string(got) != payloads[i] {
			t.Errorf("id[%d] payload: got %d bytes, want %d", i, len(got), len(payloads[i]))
		}
	}
}

// --- encrypted manifest confidentiality (bounded; KDF cost rules out fuzzing) ---

// TestStore_EncryptedManifestConfidentiality pins what ManifestCrypto
// guarantees — manifest-FIELD confidentiality — across modes: round-trip with
// the right passphrase; the usr metadata never appears in the on-disk manifest
// bytes; opening with the wrong passphrase fails closed.
//
// Scope: ManifestCrypto encrypts the MANIFEST, not the blob payload.
// Blob-at-rest encryption is a separate axis (StoreConfig.Pipeline) covered in
// pipeline_test.go.
func TestStore_EncryptedManifestConfidentiality(t *testing.T) {
	const usrSecret = "do-not-leak-usr-value"

	cases := []struct {
		mode config.ManifestCrypto
	}{
		{config.ManifestCryptoSealed},
		{config.ManifestCryptoParanoid},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.mode), func(t *testing.T) {
			cfg := config.StoreConfig{ManifestCrypto: tc.mode}
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
			id, err := s.Put(ctx, art)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}

			// Round-trip with the right key.
			if got := storekit.GetBytes(t, s, id); !bytes.Equal(got, payload) {
				t.Fatalf("encrypted round-trip mismatch")
			}

			// Inspect the raw manifest file on disk.
			mp := storefx.OnDiskAt(r.Root()).ManifestPath(storekit.MustDigest(t, s, id))
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
