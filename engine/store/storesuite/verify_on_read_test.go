// VerifyOnRead: per-policy blob verification on the Get path. The policy
// matrix (ForceEnabled / Disabled / Auto) decides whether on-disk
// corruption is detected as ErrCorruptedBlob during Read or passed through
// silently; clean blobs round-trip under ForceEnabled for both target and
// inline layouts; a detected mismatch emits EventScrubFailed. Event
// capture goes through eventfx.Recorder (lastScrubPayload lives in
// verify_test.go).

package storesuite

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

// configWith pins VerifyOnRead to a policy, leaving every other field to
// config defaults so the only variable across cases is the policy.
func configWith(policy domain.VerifyOnReadPolicy) domain.StoreConfig {
	return domain.StoreConfig{VerifyOnRead: policy}
}

// corruptBlob flips the first byte of the sole blob file under root.
// Assumes exactly one blob — every test here writes one artifact first.
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

// TestGet_VerifyOnRead_Policy: with the blob corrupted on disk, the policy
// decides the Read outcome. ForceEnabled and Auto (on a plain blob) detect
// it as ErrCorruptedBlob; Disabled reads through silently (no error, but
// the bytes reflect the corruption).
func TestGet_VerifyOnRead_Policy(t *testing.T) {
	const content = "verify on read policy"
	cases := []struct {
		name          string
		policy        domain.VerifyOnReadPolicy
		wantCorrupted bool
	}{
		{"force enabled detects", domain.VerifyOnReadForceEnabled, true},
		{"disabled is silent", domain.VerifyOnReadDisabled, false},
		{"auto on plain blob detects", domain.VerifyOnReadAuto, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, root := storefx.InitWithRoot(t, store.WithConfig(configWith(tc.policy)))
			id, err := s.Put(context.Background(), artifactfx.Payload(content))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			corruptBlob(t, root)

			rh, err := s.Get(context.Background(), id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer rh.Close()
			got, readErr := io.ReadAll(rh)

			if tc.wantCorrupted {
				if !errors.Is(readErr, errs.ErrCorruptedBlob) {
					t.Fatalf("%s: expected errs.ErrCorruptedBlob, got %v", tc.name, readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatalf("%s: expected silent read, got error: %v", tc.name, readErr)
			}
			if string(got) == content {
				t.Fatalf("%s: blob was not actually corrupted on disk", tc.name)
			}
		})
	}
}

// TestGet_VerifyOnRead_CleanRoundtrip: under ForceEnabled, an untampered
// blob round-trips byte-identically for both the target and inline layouts
// — guarding against false positives in the wrap-and-rehash path.
func TestGet_VerifyOnRead_CleanRoundtrip(t *testing.T) {
	const want = "clean blob no tamper"
	cases := []struct {
		name string
		init func(t *testing.T) store.Store
	}{
		{"target", func(t *testing.T) store.Store {
			s, _ := storefx.InitWithRoot(t,
				store.WithConfig(configWith(domain.VerifyOnReadForceEnabled)))
			return s
		}},
		{"inline", func(t *testing.T) store.Store {
			cfg := configWith(domain.VerifyOnReadForceEnabled)
			cfg.InlineBlobLimit = 1024
			s, _ := storefx.InitWithRoot(t, store.WithConfig(cfg))
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.init(t)
			id, err := s.Put(context.Background(), artifactfx.Payload(want))
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
			if string(got) != want {
				t.Fatalf("%s roundtrip: got %q, want %q", tc.name, got, want)
			}
		})
	}
}

// TestGet_VerifyOnRead_EmitsScrubFailedEvent: a mismatch detected on the
// Get path publishes exactly one EventScrubFailed carrying the ArtifactID
// and an ErrCorruptedBlob cause.
func TestGet_VerifyOnRead_EmitsScrubFailedEvent(t *testing.T) {
	rec := eventfx.New()
	s, root := storefx.InitWithRoot(t,
		store.WithConfig(configWith(domain.VerifyOnReadForceEnabled)),
		store.WithPublisher(rec),
	)
	id, err := s.Put(context.Background(), artifactfx.Payload("event must fire"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	corruptBlob(t, root)

	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	_, _ = io.ReadAll(rh) // the read error itself is covered by TestGet_VerifyOnRead_Policy

	if n := rec.Count(event.EventScrubFailed); n != 1 {
		t.Fatalf("EventScrubFailed: got %d events, want 1", n)
	}
	p := lastScrubPayload(t, rec)
	if p.ArtifactID != id {
		t.Errorf("EventScrubFailed.ArtifactID: got %q, want %q", p.ArtifactID, id)
	}
	if !errors.Is(p.Err, errs.ErrCorruptedBlob) {
		t.Errorf("EventScrubFailed.Err: %v (want errs.ErrCorruptedBlob)", p.Err)
	}
}
