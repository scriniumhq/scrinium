package store_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/internal/testutil/storefx"
	"scrinium.dev/engine/plugin/crypto/aesgcm"
	"scrinium.dev/engine/plugins"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
)

// TestPut_ConvergentBlobsDedup is the positive counterpart to
// TestPut_EncryptedBlobsDoNotDedup. With EncryptedDedup=Convergent
// (ADR-58/59) the segmented AEAD encoder derives every segment IV
// deterministically from (DEK, segment plaintext, KeyID, index), so
// the same plaintext under the same key produces byte-identical
// ciphertext, an identical BlobRef, and therefore a single physical
// blob — while each Put still writes its own manifest.
//
// The pinned-DEK factory records an empty KeyID, so the convergent IV
// derivation is keyed only by the DEK; that is sufficient for the
// dedup probe (which takes the encrypted branch on a non-empty
// crypto-identity and resolves by BlobRef equality).
func TestPut_ConvergentBlobsDedup(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i)
	}
	aesFactory, err := aesgcm.New(dek)
	if err != nil {
		t.Fatalf("aesgcm.New: %v", err)
	}
	reg := plugins.NewTransformerRegistry().Register("aes-gcm", aesFactory)

	cfg := domain.StoreConfig{
		Pipeline:       []string{"aes-gcm"},
		EncryptedDedup: domain.EncryptedDedupConvergent,
		// SegmentSize left zero: ApplyDefaults sets it to
		// DefaultSegmentSize for this encrypting store.
	}
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	s := storefx.InitOn(t, drv,
		store.WithStoreIndex(idx),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)

	const samePayload = "convergent dedup payload"
	ids := make([]domain.ArtifactID, 0, 3)
	for i := 0; i < 3; i++ {
		a, _ := payloadReader(samePayload)
		id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "ns"})
		if err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// (a) Exactly one blob on disk — convergent ciphertext is
	// reproducible, so the 2nd and 3rd writes deduplicate onto the
	// first. This is the assertion that reads "3" under Disabled.
	disk := storefx.OnDiskAt(drv.Root())
	if blobCount := disk.BlobCount(); blobCount != 1 {
		t.Errorf("Convergent: 3 Puts of same plaintext should yield 1 blob, got %d", blobCount)
	}

	// (b) Every manifest is independently readable — they all point at
	// the one shared blob and decrypt cleanly (round-trip on the
	// deduplicated blob).
	for i, id := range ids {
		rh, err := s.Get(context.Background(), id, domain.GetOptions{})
		if err != nil {
			t.Fatalf("Get id[%d]: %v", i, err)
		}
		got, _ := io.ReadAll(rh)
		_ = rh.Close()
		if string(got) != samePayload {
			t.Errorf("id[%d] payload: got %q, want %q", i, got, samePayload)
		}
	}
}

// TestPut_ConvergentMultiSegmentDedup exercises a payload that spans
// several segments to confirm the per-segment convergent derivation
// (segment index folded into each IV) is still reproducible end to
// end: two identical large payloads collapse to one blob.
func TestPut_ConvergentMultiSegmentDedup(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(7 * i)
	}
	aesFactory, err := aesgcm.New(dek)
	if err != nil {
		t.Fatalf("aesgcm.New: %v", err)
	}
	reg := plugins.NewTransformerRegistry().Register("aes-gcm", aesFactory)

	cfg := domain.StoreConfig{
		Pipeline:       []string{"aes-gcm"},
		EncryptedDedup: domain.EncryptedDedupConvergent,
		SegmentSize:    4096, // force many segments for a modest payload
	}
	drv := driverfx.LocalFS(t)
	s := storefx.InitOn(t, drv,
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)

	payload := make([]byte, 4096*5+123)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	put := func() domain.ArtifactID {
		a := domain.Artifact{Payload: bytes.NewReader(payload)}
		id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "ns"})
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		return id
	}
	id1 := put()
	_ = put()

	if bc := storefx.OnDiskAt(drv.Root()).BlobCount(); bc != 1 {
		t.Errorf("Convergent multi-segment: want 1 blob, got %d", bc)
	}

	rh, err := s.Get(context.Background(), id1, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if len(got) != len(payload) {
		t.Fatalf("round-trip length: got %d, want %d", len(got), len(payload))
	}
	for i := range got {
		if got[i] != payload[i] {
			t.Fatalf("round-trip mismatch at byte %d", i)
		}
	}
}
