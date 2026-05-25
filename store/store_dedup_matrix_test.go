package store_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
	"scrinium.dev/internal/testutil/storefx"
	"scrinium.dev/store"
	"scrinium.dev/store/pipeline"
	"scrinium.dev/store/pipeline/stage/aesgcm"
)

// TestDedup_Matrix is the single source of truth for the blob
// deduplication decision at the Store layer (ADR-29 + ADR-58/59). It
// is the Tier-2 invariant that closes out the dedup work spread across
// R7 (crypto-identity in the key), R8 (segmented AEAD + Convergent),
// and R8.5 (unified key). Each row writes a sequence of payloads under
// one Store config and asserts two things:
//
//  1. the number of *physical* blobs on disk matches the dedup policy;
//  2. every artifact still reads back its exact payload — the
//     no-data-loss invariant the M2 encrypted-dedup bug violated.
//
// The matrix covers everything observable at the Store layer with a
// single DEK (pinned aes-gcm). Two cross-config invariants live one
// layer down, in the index conformance suite (indextest), because a
// single Store cannot host both at once:
//
//   - Plain vs encrypted of identical content never collapse, and
//   - a different KeyID never collapses,
//
// both asserted by run_exists_by_content.go and run_exists_by_hash.go
// ("CryptoIdentitySplitsKey").
//
// Targeted siblings keep extra assertions the matrix does not make:
// TestPut_TwoArtifactsShareBlob_RefCountIs2 (ref_count), and
// TestPut_EncryptedBlobsDoNotDedup (delete-survivor semantics, the
// named M2 regression).
func TestDedup_Matrix(t *testing.T) {
	// A payload large enough to span several 4 KiB segments, used to
	// prove the per-segment Convergent IV derivation stays reproducible
	// across segment boundaries (ADR-59).
	multiSegment := strings.Repeat("scrinium-convergent-", 600) // ~12 KiB

	cases := []struct {
		name        string
		encrypted   bool // wire a pinned aes-gcm crypto stage
		dedup       domain.EncryptedDedup
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
			dedup:     domain.EncryptedDedupDisabled,
			payloads:  []string{"secret", "secret", "secret"},
			wantBlobs: 3, // random IV per write → 3 distinct ciphertexts
		},
		{
			name:      "EncryptedDisabled/DifferentContent_distinct",
			encrypted: true,
			dedup:     domain.EncryptedDedupDisabled,
			payloads:  []string{"secret-a", "secret-b"},
			wantBlobs: 2,
		},
		{
			name:      "EncryptedConvergent/SameContent_dedups",
			encrypted: true,
			dedup:     domain.EncryptedDedupConvergent,
			payloads:  []string{"converge", "converge", "converge"},
			wantBlobs: 1, // deterministic IV → identical ciphertext → one BlobRef
		},
		{
			name:      "EncryptedConvergent/DifferentContent_distinct",
			encrypted: true,
			dedup:     domain.EncryptedDedupConvergent,
			payloads:  []string{"converge-a", "converge-b"},
			wantBlobs: 2,
		},
		{
			name:        "EncryptedConvergent/MultiSegment_dedups",
			encrypted:   true,
			dedup:       domain.EncryptedDedupConvergent,
			segmentSize: 4096,
			payloads:    []string{multiSegment, multiSegment},
			wantBlobs:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			drv := driverfx.LocalFS(t)

			cfg := domain.StoreConfig{}
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
					store.WithNamespace("ns"))
				if err != nil {
					t.Fatalf("Put #%d: %v", i, err)
				}
				ids[i] = id
			}

			// (1) physical blob count matches the dedup policy.
			if got := storefx.OnDiskAt(drv.Root()).BlobCount(); got != tc.wantBlobs {
				t.Errorf("BlobCount = %d, want %d", got, tc.wantBlobs)
			}

			// (2) no-data-loss: every artifact reads back its payload,
			// whether it owns a blob or shares a deduplicated one.
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
