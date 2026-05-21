package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/internal/testutil/storefx"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
)

// scrubCapture collects every ScrubFailedPayload published by the
// store. Used by the divergence tests to assert the documented
// "Verify emits EventScrubFailed on hash mismatch" contract.
type scrubCapture struct {
	mu       sync.Mutex
	payloads []event.ScrubFailedPayload
}

func newScrubCapture() *scrubCapture { return &scrubCapture{} }

func (c *scrubCapture) handle(e event.Event) {
	if e.Type != event.EventScrubFailed {
		return
	}
	p, ok := e.Payload.(event.ScrubFailedPayload)
	if !ok {
		return
	}
	c.mu.Lock()
	c.payloads = append(c.payloads, p)
	c.mu.Unlock()
}

func (c *scrubCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.payloads)
}

func (c *scrubCapture) last(t *testing.T) event.ScrubFailedPayload {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.payloads) == 0 {
		t.Fatalf("EventScrubFailed: no events seen")
	}
	return c.payloads[len(c.payloads)-1]
}

// --- Happy path ---

func TestVerify_TargetBlob_Roundtrip(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("verify me"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Verify(context.Background(), id); err != nil {
		t.Errorf("Verify after fresh Put: %v", err)
	}
}

func TestVerify_InlineBlob_Roundtrip(t *testing.T) {
	s, _ := newInlineStore(t, 1024)
	id, err := s.Put(context.Background(),
		payload("inline data"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Verify(context.Background(), id); err != nil {
		t.Errorf("Verify on inline artifact: %v", err)
	}
}

// --- Divergence ---

func TestVerify_TargetBlob_TamperedBytes_ReturnsCorruptedBlob(t *testing.T) {
	bus := event.NewEventBus()
	scrub := newScrubCapture()
	bus.Subscribe(scrub.handle)

	s, root := storefx.InitWithRoot(t, core.WithPublisher(bus))
	id, err := s.Put(context.Background(),
		payload("tamper target"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read the manifest off-disk so we can find the BlobRef and
	// flip a byte in the corresponding blob file.
	blobRef := readBlobRef(t, s, id)
	blobPath := filepath.Join(root, blobPathForRef(t, string(blobRef)))
	content, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if len(content) == 0 {
		t.Fatalf("blob unexpectedly empty")
	}
	content[0] ^= 0xff
	if err := os.WriteFile(blobPath, content, 0o644); err != nil {
		t.Fatalf("write tampered blob: %v", err)
	}

	err = s.Verify(context.Background(), id)
	if !errors.Is(err, errs.ErrCorruptedBlob) {
		t.Fatalf("expected errs.ErrCorruptedBlob, got %v", err)
	}
	if scrub.count() != 1 {
		t.Fatalf("EventScrubFailed: got %d events, want 1", scrub.count())
	}
	if got := scrub.last(t).ArtifactID; got != id {
		t.Errorf("payload.ArtifactID: got %q, want %q", got, id)
	}
}

func TestVerify_TargetBlob_Missing_ReturnsCorruptedBlob(t *testing.T) {
	bus := event.NewEventBus()
	scrub := newScrubCapture()
	bus.Subscribe(scrub.handle)

	s, root := storefx.InitWithRoot(t, core.WithPublisher(bus))
	id, err := s.Put(context.Background(),
		payload("delete the blob"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	blobRef := readBlobRef(t, s, id)
	blobPath := filepath.Join(root, blobPathForRef(t, string(blobRef)))
	if err := os.Remove(blobPath); err != nil {
		t.Fatalf("remove blob: %v", err)
	}

	err = s.Verify(context.Background(), id)
	if !errors.Is(err, errs.ErrCorruptedBlob) {
		t.Fatalf("expected errs.ErrCorruptedBlob, got %v", err)
	}
	if scrub.count() != 1 {
		t.Fatalf("EventScrubFailed: got %d events, want 1", scrub.count())
	}
}

// --- Argument and state validation ---

func TestVerify_EmptyID_ReturnsArtifactNotFound(t *testing.T) {
	s := newStore(t)
	if err := s.Verify(context.Background(), ""); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("got %v, want errs.ErrArtifactNotFound", err)
	}
}

func TestVerify_UnknownID_ReturnsArtifactNotFound(t *testing.T) {
	s := newStore(t)
	err := s.Verify(context.Background(), domain.ArtifactID("sha256-deadbeef"))
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("got %v, want errs.ErrArtifactNotFound", err)
	}
}

func TestVerify_OfflineMode_Blocked(t *testing.T) {
	s := newStore(t)
	id, err := s.Put(context.Background(),
		payload("offline test"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.SetMaintenanceMode(context.Background(),
		domain.MaintenanceModeOffline); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(context.Background(), id); !errors.Is(err, errs.ErrStoreOffline) {
		t.Errorf("got %v, want errs.ErrStoreOffline", err)
	}
}

func TestVerify_CancelledContext(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Verify(ctx, domain.ArtifactID("sha256-deadbeef"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

// readBlobRef reads the manifest from the Store and returns the
// BlobRef. Implementation: open the artifact via Get and read the
// manifest off the resulting handle.
func readBlobRef(t *testing.T, s core.Store, id domain.ArtifactID) domain.BlobRef {
	t.Helper()
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get for manifest: %v", err)
	}
	defer rh.Close()
	return rh.Manifest().BlobRef
}

// --- M2.3: encrypted manifest is transparent to Verify ---

func TestVerify_EncryptedManifest_Succeeds(t *testing.T) {
	for _, crypto := range []domain.ManifestCrypto{
		domain.ManifestCryptoSealed,
		domain.ManifestCryptoParanoid,
	} {
		t.Run(string(crypto), func(t *testing.T) {
			drv := driverfx.LocalFS(t)
			idx := indexfx.Memory(t)
			cfg := domain.StoreConfig{ManifestCrypto: crypto}

			if _, _, err := core.InitStore(context.Background(), drv,
				core.WithConfig(cfg),
				core.WithPassphrase(storefx.StaticPP("pw")),
				core.WithStoreIndex(idx),
				core.WithHashRegistry(storefx.Hashes()),
			); err != nil {
				t.Fatalf("InitStore: %v", err)
			}
			s, err := core.OpenStore(context.Background(), drv,
				core.WithConfig(cfg),
				core.WithPassphrase(storefx.StaticPP("pw")),
				core.WithAutoUnlock(),
				core.WithStoreIndex(idx),
				core.WithHashRegistry(storefx.Hashes()),
			)
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}

			id, err := s.Put(context.Background(),
				payload("verify encrypted"),
				domain.PutOptions{Namespace: "v"})
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := s.Verify(context.Background(), id); err != nil {
				t.Fatalf("Verify: %v", err)
			}
		})
	}
}
