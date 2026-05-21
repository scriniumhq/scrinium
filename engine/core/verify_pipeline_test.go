package core_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/internal/testutil/storefx"
	scriniumzstd "scrinium.dev/engine/plugin/compress/zstd"
	"scrinium.dev/engine/plugin/crypto/aesgcm"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
)

// --- Helpers ---

// initPipelineStore builds a Store with the given Pipeline and a
// matching TransformerRegistry. Returns the Store and the driver
// root for on-disk inspection.
func initPipelineStore(
	t *testing.T,
	reg coreapi.TransformerRegistry,
	pipeline []string,
	extra ...core.StoreOption,
) (core.Store, string) {
	t.Helper()
	cfg := domain.StoreConfig{Pipeline: pipeline}
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	opts := append([]core.StoreOption{
		core.WithStoreIndex(idx),
		core.WithReadRegistry(reg),
		core.WithConfig(cfg),
	}, extra...)
	s := storefx.InitOn(t, drv, opts...)
	return s, drv.Root()
}

// pipelineBlobPath returns the on-disk path of the (sole) blob
// produced by a pipeline-bearing Put. The pipeline writes one
// transformed blob per artifact; the helper resolves its path
// through the manifest's BlobRef so the tests don't hardcode the
// shard rule.
func pipelineBlobPath(t *testing.T, s core.Store, root string, id domain.ArtifactID) string {
	t.Helper()
	ref := readBlobRef(t, s, id)
	return filepath.Join(root, blobPathForRef(t, string(ref)))
}

// --- Happy path: pipeline-bearing Verify succeeds ---

func TestVerify_Pipeline_Zstd_Succeeds(t *testing.T) {
	reg := core.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))
	s, _ := initPipelineStore(t, reg, []string{"zstd"})

	// Highly compressible input so the pipeline actually does
	// non-trivial work (the on-disk bytes differ from the
	// plaintext).
	original := bytes.Repeat([]byte("scrinium verify "), 1024)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerify_Pipeline_AESGCM_Succeeds(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i)
	}
	aesFactory, err := aesgcm.New(dek)
	if err != nil {
		t.Fatalf("aesgcm.New: %v", err)
	}
	reg := core.NewTransformerRegistry().Register("aes-gcm", aesFactory)
	s, _ := initPipelineStore(t, reg, []string{"aes-gcm"})

	original := []byte("encrypted blob to verify")
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerify_Pipeline_ZstdThenAESGCM_Succeeds(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i)
	}
	aesFactory, _ := aesgcm.New(dek)
	reg := core.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{})).
		Register("aes-gcm", aesFactory)
	s, _ := initPipelineStore(t, reg, []string{"zstd", "aes-gcm"})

	original := bytes.Repeat([]byte("two-stage "), 512)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// --- Tier 1 fault injection: tampered ciphertext under AEAD ---
//
// AEAD.Open fails on the first Read of the wrapped reader, well
// before the hasher finishes. Verify must fold that into
// ErrCorruptedBlob (admin-side category: "blob is not whole")
// and publish EventScrubFailed.

func TestVerify_Pipeline_AESGCM_TamperedCiphertext_ReturnsCorruptedBlob(t *testing.T) {
	bus := event.NewEventBus()
	scrub := newScrubCapture()
	bus.Subscribe(scrub.handle)

	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i)
	}
	aesFactory, _ := aesgcm.New(dek)
	reg := core.NewTransformerRegistry().Register("aes-gcm", aesFactory)
	s, root := initPipelineStore(t, reg, []string{"aes-gcm"},
		core.WithPublisher(bus))

	// 1 KiB of repeating bytes — big enough that len/2 lands
	// well inside the ciphertext payload, far from the trailing
	// 16-byte GCM tag.
	original := bytes.Repeat([]byte("tamper-target "), 64)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Flip a byte in the middle of the ciphertext (well before
	// the trailing 16-byte tag).
	blobPath := pipelineBlobPath(t, s, root, id)
	raw, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	raw[len(raw)/2] ^= 0x01
	if err := os.WriteFile(blobPath, raw, 0o644); err != nil {
		t.Fatalf("rewrite blob: %v", err)
	}

	err = s.Verify(context.Background(), id)
	if !errors.Is(err, errs.ErrCorruptedBlob) {
		t.Fatalf("expected errs.ErrCorruptedBlob, got %v", err)
	}
	if scrub.count() != 1 {
		t.Fatalf("EventScrubFailed: got %d events, want 1", scrub.count())
	}
	if got := scrub.last(t).ArtifactID; got != id {
		t.Errorf("EventScrubFailed.ArtifactID: got %q, want %q", got, id)
	}
}

// --- Tampered ciphertext under plain zstd: hash diverges ---
//
// Zstd is not authenticated. A flipped byte may produce either a
// decoder error (most likely — zstd's framing breaks) or a
// successful decode of garbage. Both must surface as
// ErrCorruptedBlob.

func TestVerify_Pipeline_Zstd_TamperedCiphertext_ReturnsCorruptedBlob(t *testing.T) {
	bus := event.NewEventBus()
	scrub := newScrubCapture()
	bus.Subscribe(scrub.handle)

	reg := core.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))
	s, root := initPipelineStore(t, reg, []string{"zstd"},
		core.WithPublisher(bus))

	original := bytes.Repeat([]byte("compressible "), 256)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	blobPath := pipelineBlobPath(t, s, root, id)
	raw, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	// Flip a byte deep inside the frame; the zstd header at the
	// very start has its own integrity checks we'd rather not
	// trip on.
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(blobPath, raw, 0o644); err != nil {
		t.Fatalf("rewrite blob: %v", err)
	}

	err = s.Verify(context.Background(), id)
	if !errors.Is(err, errs.ErrCorruptedBlob) {
		t.Fatalf("expected errs.ErrCorruptedBlob, got %v", err)
	}
	if scrub.count() != 1 {
		t.Fatalf("EventScrubFailed: got %d events, want 1", scrub.count())
	}
}

// --- Missing blob file under a pipeline-bearing manifest ---
//
// The driver returns os.ErrNotExist; verify maps that to
// ErrCorruptedBlob the same way the non-pipeline path does.

func TestVerify_Pipeline_MissingBlob_ReturnsCorruptedBlob(t *testing.T) {
	reg := core.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))
	s, root := initPipelineStore(t, reg, []string{"zstd"})

	original := bytes.Repeat([]byte("gone "), 128)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	blobPath := pipelineBlobPath(t, s, root, id)
	if err := os.Remove(blobPath); err != nil {
		t.Fatalf("remove blob: %v", err)
	}

	err = s.Verify(context.Background(), id)
	if !errors.Is(err, errs.ErrCorruptedBlob) {
		t.Fatalf("expected errs.ErrCorruptedBlob, got %v", err)
	}
}

// --- Round-trip: Verify is consistent with Get-then-ReadAll ---
//
// After a successful Verify, the plaintext returned by Get must
// match the original bytes. Guards against a hypothetical bug
// where Verify hashes one stream and Get returns a different one
// (different pipeline state, different ordering).

func TestVerify_Pipeline_ConsistentWithGet(t *testing.T) {
	reg := core.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))
	s, _ := initPipelineStore(t, reg, []string{"zstd"})

	original := bytes.Repeat([]byte("consistency "), 512)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("Get plaintext differs from original (len got=%d want=%d)",
			len(got), len(original))
	}
}
