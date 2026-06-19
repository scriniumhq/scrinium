// StoreConfig.Pipeline: the blob payload transform chain (compression /
// encryption) applied on Put and reversed on Get. Consolidated onto the
// pipelinefx fixture as a table over the supported stage chains, and
// extended with the blob-at-rest confidentiality property the per-algorithm
// example tests lacked. The subject is the pipeline, not Put mechanics.

package storesuite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/pipeline"
	zstdstage "scrinium.dev/engine/pipeline/stage/zstd"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/pipelinefx"
	"scrinium.dev/testutil/storefx"
)

// TestPipeline_RoundTrip: content survives Put/Get through each supported
// blob pipeline, the manifest records the stages in order, and a non-empty
// pipeline disables random access (the blob is a transformed stream, not the
// raw payload).
func TestPipeline_RoundTrip(t *testing.T) {
	cases := [][]string{
		{"zstd"},
		{"aes-gcm"},
		{"zstd", "aes-gcm"},
	}
	for _, algos := range cases {
		t.Run(strings.Join(algos, "+"), func(t *testing.T) {
			s, _ := pipelinefx.Stack(t, algos...)
			original := bytes.Repeat([]byte("scrinium pipeline payload "), 512)

			id, err := s.Put(context.Background(),
				domain.Artifact{Payload: bytes.NewReader(original)},
			)
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
			if !bytes.Equal(got, original) {
				t.Fatalf("round-trip mismatch (got %d bytes, want %d)", len(got), len(original))
			}

			// A transformed stream cannot be range-read.
			if rh.SupportsRandomAccess() {
				t.Errorf("SupportsRandomAccess must be false for a non-empty pipeline")
			}
			if _, err := rh.ReadAt(make([]byte, 8), 0); !errors.Is(err, errs.ErrRandomAccessNotSupported) {
				t.Errorf("ReadAt: got %v, want errs.ErrRandomAccessNotSupported", err)
			}

			// Manifest records the stages in order.
			m := rh.Manifest()
			if len(m.Pipeline) != len(algos) {
				t.Fatalf("manifest Pipeline = %+v, want %d stage(s)", m.Pipeline, len(algos))
			}
			for i, a := range algos {
				if m.Pipeline[i].Algorithm != a {
					t.Errorf("stage %d: got %q, want %q", i, m.Pipeline[i].Algorithm, a)
				}
				// Segmented AEAD keeps per-segment IVs in the blob body, so
				// the manifest stage IV is empty (ADR-59).
				if a == "aes-gcm" && len(m.Pipeline[i].IV) != 0 {
					t.Errorf("aes-gcm stage IV len = %d, want 0 (IVs live in frames)", len(m.Pipeline[i].IV))
				}
			}
			if m.OriginalSize != int64(len(original)) {
				t.Errorf("OriginalSize = %d, want %d", m.OriginalSize, len(original))
			}
		})
	}
}

// TestPipeline_BlobConfidentialityAtRest: with an aes-gcm pipeline the
// plaintext must not appear in the blob bytes on disk. This is the
// blob-encryption-at-rest guarantee, distinct from ManifestCrypto (which
// encrypts manifest fields, not the payload). The plain-store control proves
// the assertion has teeth — it would catch a leak rather than passing
// vacuously.
func TestPipeline_BlobConfidentialityAtRest(t *testing.T) {
	marker := []byte("TOP-SECRET-PLAINTEXT-MARKER-7f3a9c2e")

	t.Run("aes-gcm hides plaintext at rest", func(t *testing.T) {
		s, root := pipelinefx.Stack(t, "aes-gcm")
		id, err := s.Put(context.Background(),
			domain.Artifact{Payload: bytes.NewReader(marker)},
		)
		if err != nil {
			t.Fatalf("Put: %v", err)
		}

		blobs := storefx.OnDiskAt(root).BlobFiles()
		if len(blobs) == 0 {
			t.Fatal("no blob files on disk — cannot verify confidentiality")
		}
		for _, p := range blobs {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read blob %s: %v", p, err)
			}
			if bytes.Contains(raw, marker) {
				t.Fatalf("plaintext marker present in encrypted blob at rest: %s", p)
			}
		}

		// Confidentiality must not cost correctness: it still round-trips.
		rh, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rh.Close()
		got, _ := io.ReadAll(rh)
		if !bytes.Equal(got, marker) {
			t.Fatalf("round-trip mismatch through aes-gcm")
		}
	})

	t.Run("plain store leaks plaintext (control)", func(t *testing.T) {
		s, root := storefx.InitWithRoot(t)
		if _, err := s.Put(context.Background(),
			domain.Artifact{Payload: bytes.NewReader(marker)},
		); err != nil {
			t.Fatalf("Put: %v", err)
		}
		found := false
		for _, p := range storefx.OnDiskAt(root).BlobFiles() {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read blob %s: %v", p, err)
			}
			if bytes.Contains(raw, marker) {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("control failed: plaintext not found in a plain blob — the at-rest check cannot detect leaks")
		}
	})
}

// TestPipeline_ConfigGuards: a pipeline naming an unregistered algorithm is
// refused (ErrUnsupportedAlgorithm), and a pipeline combined with inline
// storage is never silently accepted — refused either at InitStore or at Put.
func TestPipeline_ConfigGuards(t *testing.T) {
	t.Run("unregistered algorithm", func(t *testing.T) {
		reg := pipeline.NewTransformerRegistry() // empty: "zstd" not registered
		s, _ := storefx.InitWithRoot(t,
			store.WithReadRegistry(reg),
			store.WithConfig(domain.StoreConfig{Pipeline: []string{"zstd"}}))
		_, err := s.Put(context.Background(),
			domain.Artifact{Payload: bytes.NewReader([]byte("x"))},
		)
		if !errors.Is(err, errs.ErrUnsupportedAlgorithm) {
			t.Fatalf("Put: got %v, want errs.ErrUnsupportedAlgorithm", err)
		}
	})

	t.Run("pipeline plus inline is refused", func(t *testing.T) {
		reg := pipeline.NewTransformerRegistry().
			Register("zstd", zstdstage.New(zstdstage.Options{}))
		cfg := domain.StoreConfig{
			Pipeline:        []string{"zstd"},
			BlobStorage:     domain.BlobStorageInline,
			InlineBlobLimit: 1024,
		}
		s, _, err := store.InitStore(context.Background(), driverfx.LocalFS(t),
			store.WithStoreIndex(indexfx.Memory(t)),
			store.WithHashRegistry(storefx.Hashes()),
			store.WithReadRegistry(reg),
			store.WithConfig(cfg))
		if err != nil {
			// Refused at config validation — also a valid outcome; the engine
			// just must never silently accept the combination.
			t.Skipf("InitStore refused inline+pipeline at startup: %v", err)
		}
		if _, err := s.Put(context.Background(),
			domain.Artifact{Payload: bytes.NewReader([]byte("x"))},
		); err == nil {
			t.Fatal("Put: expected refusal for inline+pipeline, got nil")
		}
	})
}
