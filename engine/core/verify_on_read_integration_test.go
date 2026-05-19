package core_test

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/internal/testutil/storefx"
)

// localScrubCapture mirrors verify_test.go's scrubCapture for use
// inside this file. The original is unexported; duplicating the
// shape keeps the two test files independent (the verify-suite
// can move under a different build tag in the future without
// dragging VerifyOnRead tests along).
type localScrubCapture struct {
	mu       sync.Mutex
	payloads []core.ScrubFailedPayload
}

func (c *localScrubCapture) handle(e event.Event) {
	if e.Type != core.EventScrubFailed {
		return
	}
	p, ok := e.Payload.(core.ScrubFailedPayload)
	if !ok {
		return
	}
	c.mu.Lock()
	c.payloads = append(c.payloads, p)
	c.mu.Unlock()
}

func (c *localScrubCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.payloads)
}

func (c *localScrubCapture) last(t *testing.T) core.ScrubFailedPayload {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.payloads) == 0 {
		t.Fatalf("EventScrubFailed: no events seen")
	}
	return c.payloads[len(c.payloads)-1]
}

// configWith returns a StoreConfig that pins VerifyOnRead to the
// given policy and otherwise relies on config_default to fill
// every other field. Used in every test below so the only
// variable across cases is the policy itself.
func configWith(policy domain.VerifyOnReadPolicy) domain.StoreConfig {
	return domain.StoreConfig{VerifyOnRead: policy}
}

// corruptBlob flips the first byte of the (single) blob file
// under the store root. Assumes exactly one blob — every test
// here writes one artifact before calling this helper.
func corruptBlob(t *testing.T, root string) {
	t.Helper()
	files := storefx.OnDiskAt(root).BlobFiles()
	if len(files) != 1 {
		t.Fatalf("expected 1 blob file, got %d", len(files))
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("blob unexpectedly empty")
	}
	raw[0] ^= 0x01
	if err := os.WriteFile(files[0], raw, 0o644); err != nil {
		t.Fatalf("rewrite blob: %v", err)
	}
}

// --- ForceEnabled detects on-disk corruption ---

func TestGet_VerifyOnRead_ForceEnabled_DetectsBlobCorruption(t *testing.T) {
	s, root := storefx.InitWithRoot(t,
		core.WithConfig(configWith(domain.VerifyOnReadForceEnabled)),
	)
	id, err := s.Put(context.Background(),
		payload("hello verify on read"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	corruptBlob(t, root)

	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	_, err = io.ReadAll(rh)
	if !errors.Is(err, errs.ErrCorruptedBlob) {
		t.Fatalf("expected errs.ErrCorruptedBlob, got %v", err)
	}
}

// --- Disabled does NOT detect corruption ---

func TestGet_VerifyOnRead_Disabled_SilentOnCorruption(t *testing.T) {
	s, root := storefx.InitWithRoot(t,
		core.WithConfig(configWith(domain.VerifyOnReadDisabled)),
	)
	id, err := s.Put(context.Background(),
		payload("trust the medium"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	corruptBlob(t, root)

	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("expected silent read, got error: %v", err)
	}
	if string(got) == "trust the medium" {
		t.Fatalf("blob was not actually corrupted on disk")
	}
}

// --- Auto, plain pipeline, plain media: verifies ---

func TestGet_VerifyOnRead_Auto_PlainBlob_Verifies(t *testing.T) {
	// Auto is the default; passing it explicitly here documents
	// the case under test.
	s, root := storefx.InitWithRoot(t,
		core.WithConfig(configWith(domain.VerifyOnReadAuto)),
	)
	id, err := s.Put(context.Background(),
		payload("auto must catch this"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	corruptBlob(t, root)

	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	_, err = io.ReadAll(rh)
	if !errors.Is(err, errs.ErrCorruptedBlob) {
		t.Fatalf("Auto on plain blob: expected errs.ErrCorruptedBlob, got %v", err)
	}
}

// --- Happy path: clean blob passes through under ForceEnabled ---

func TestGet_VerifyOnRead_ForceEnabled_CleanBlobRoundtrip(t *testing.T) {
	s, _ := storefx.InitWithRoot(t,
		core.WithConfig(configWith(domain.VerifyOnReadForceEnabled)),
	)
	const want = "clean blob no tamper"
	id, err := s.Put(context.Background(),
		payload(want),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
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
	if string(got) != want {
		t.Fatalf("ReadAll: got %q, want %q", got, want)
	}
}

// --- EventScrubFailed is emitted on mismatch ---

func TestGet_VerifyOnRead_EmitsScrubFailedEvent(t *testing.T) {
	bus := event.NewEventBus()
	scrub := &localScrubCapture{}
	bus.Subscribe(scrub.handle)

	s, root := storefx.InitWithRoot(t,
		core.WithConfig(configWith(domain.VerifyOnReadForceEnabled)),
		core.WithPublisher(bus),
	)
	id, err := s.Put(context.Background(),
		payload("event must fire"),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	corruptBlob(t, root)

	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	_, _ = io.ReadAll(rh) // err covered elsewhere

	if scrub.count() != 1 {
		t.Fatalf("EventScrubFailed: got %d events, want 1", scrub.count())
	}
	got := scrub.last(t)
	if got.ArtifactID != id {
		t.Errorf("EventScrubFailed.ArtifactID: got %q, want %q",
			got.ArtifactID, id)
	}
	if !errors.Is(got.Err, errs.ErrCorruptedBlob) {
		t.Errorf("EventScrubFailed.Err: %v (want errs.ErrCorruptedBlob)", got.Err)
	}
}

// --- Inline roundtrip under ForceEnabled ---
//
// Inline payloads are small enough to be embedded in the manifest
// directly (under domain.MaxInlineBlobLimit, default 0 = inline
// disabled — so we have to opt in explicitly via InlineBlobLimit).
// Wrap-and-rehash must produce the original bytes; this case
// guards against false positives on the inline path.

func TestGet_VerifyOnRead_ForceEnabled_InlineRoundtrip(t *testing.T) {
	cfg := configWith(domain.VerifyOnReadForceEnabled)
	cfg.InlineBlobLimit = 1024
	s, _ := storefx.InitWithRoot(t, core.WithConfig(cfg))

	const want = "inline body"
	id, err := s.Put(context.Background(),
		payload(want),
		domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatalf("Put: %v", err)
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
	if string(got) != want {
		t.Fatalf("inline roundtrip: got %q, want %q", got, want)
	}
}
