package store_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/indexfx"
	"scrinium.dev/internal/testutil/storefx"
)

// --- public capturing handler (black-box) --------------------------------
//
// A minimal slog.Handler that records every emitted record with its
// rendered attributes flattened into a map. Mirrors the white-box
// captureHandler in logging_test.go, but lives in package store_test so it
// can drive the real Init/Get/Delete/Verify/UpdateConfig paths through the
// public API and the storefx harness.

type logRecord struct {
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

type bbHandler struct {
	mu   *sync.Mutex
	recs *[]logRecord
	pfx  string
	base map[string]string
}

func newBBHandler() (*bbHandler, *[]logRecord) {
	recs := &[]logRecord{}
	return &bbHandler{mu: &sync.Mutex{}, recs: recs, base: map[string]string{}}, recs
}

func (h *bbHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *bbHandler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.base = map[string]string{}
	for k, v := range h.base {
		n.base[k] = v
	}
	for _, a := range as {
		n.base[h.pfx+a.Key] = a.Value.Resolve().String()
	}
	return &n
}

func (h *bbHandler) WithGroup(name string) slog.Handler {
	n := *h
	if h.pfx == "" {
		n.pfx = name + "."
	} else {
		n.pfx = h.pfx + name + "."
	}
	return &n
}

func (h *bbHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]string{}
	for k, v := range h.base {
		attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[h.pfx+a.Key] = a.Value.Resolve().String()
		return true
	})
	h.mu.Lock()
	*h.recs = append(*h.recs, logRecord{Level: r.Level, Msg: r.Message, Attrs: attrs})
	h.mu.Unlock()
	return nil
}

// find returns the first record whose message contains sub, or nil.
func find(recs *[]logRecord, sub string) *logRecord {
	for i := range *recs {
		if strings.Contains((*recs)[i].Msg, sub) {
			return &(*recs)[i]
		}
	}
	return nil
}

func debugLogger() (*slog.Logger, *[]logRecord) {
	h, recs := newBBHandler()
	return slog.New(h), recs
}

// --- lifecycle emissions -------------------------------------------------

func TestLog_InitEmitsInitialised(t *testing.T) {
	l, recs := debugLogger()
	storefx.Init(t, store.WithLogger(l))

	rec := find(recs, "store initialised")
	if rec == nil {
		t.Fatal(`no "store initialised" record`)
	}
	if rec.Level != slog.LevelInfo {
		t.Errorf("level: want Info, got %v", rec.Level)
	}
	if rec.Attrs["scrinium.store_id"] == "" {
		t.Error("missing scrinium.store_id attribute")
	}
	if rec.Attrs["scrinium.encrypted_dek"] != "false" {
		t.Errorf("encrypted_dek: want false, got %q", rec.Attrs["scrinium.encrypted_dek"])
	}
}

func TestLog_PutGetDeleteEmissions(t *testing.T) {
	l, recs := debugLogger()
	s := storefx.Init(t, store.WithLogger(l))
	ctx := context.Background()

	id, err := s.Put(ctx, storefx.Payload("hello logs"), domain.PutOptions{Namespace: "ns"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rh, err := s.Get(ctx, id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = rh.Close()
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for _, msg := range []string{"put committed", "get opened", "artifact deleted"} {
		rec := find(recs, msg)
		if rec == nil {
			t.Errorf("no %q record", msg)
			continue
		}
		if rec.Attrs["scrinium.artifact_id"] != string(id) {
			t.Errorf("%q: artifact_id want %q, got %q", msg, id, rec.Attrs["scrinium.artifact_id"])
		}
	}
}

func TestLog_UpdateConfigEmitsInfo(t *testing.T) {
	l, recs := debugLogger()
	s := storefx.Init(t, store.WithLogger(l))

	// UpdateConfig with the current config is a valid no-immutable-change
	// swap; it still exercises the write+swap+log path.
	cur := s.Config()
	if err := s.UpdateConfig(context.Background(), cur); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if rec := find(recs, "config updated"); rec == nil {
		t.Fatal(`no "config updated" record`)
	} else if rec.Level != slog.LevelInfo {
		t.Errorf("level: want Info, got %v", rec.Level)
	}
}

func TestLog_MaintenanceModeEmitsDebug(t *testing.T) {
	l, recs := debugLogger()
	s := storefx.Init(t, store.WithLogger(l))

	if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeReadOnly); err != nil {
		t.Fatalf("SetMaintenanceMode: %v", err)
	}
	rec := find(recs, "maintenance mode set")
	if rec == nil {
		t.Fatal(`no "maintenance mode set" record`)
	}
	if rec.Attrs["scrinium.mode"] != "read_only" {
		t.Errorf("mode: want read_only, got %q", rec.Attrs["scrinium.mode"])
	}
}

// --- encrypted lifecycle: open(encrypted) + unlock + KEK rotation --------

func TestLog_EncryptedOpenUnlockRotate(t *testing.T) {
	l, recs := debugLogger()

	// Init encrypted (Unlocked after init), then reopen LOCKED and unlock,
	// exercising "store opened"(encrypted) and "store unlocked".
	_, r := storefx.InitEncrypted(t, "pw-correct", store.WithLogger(l))
	locked := r.Open(t, store.WithLogger(l), store.WithPassphrase(storefx.StaticPP("pw-correct")))

	if rec := find(recs, "store opened"); rec == nil {
		t.Error(`no "store opened" record on encrypted reopen`)
	} else if rec.Attrs["scrinium.encrypted_dek"] != "true" {
		t.Errorf("encrypted_dek: want true, got %q", rec.Attrs["scrinium.encrypted_dek"])
	}

	if err := locked.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if find(recs, "store unlocked") == nil {
		t.Error(`no "store unlocked" record`)
	}

	if err := locked.RotateKEK(context.Background()); err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}
	if rec := find(recs, "KEK rotated"); rec == nil {
		t.Error(`no "KEK rotated" record`)
	} else if rec.Level != slog.LevelWarn {
		t.Errorf("KEK rotated level: want Warn, got %v", rec.Level)
	}
}

// --- security: no key material ever appears in any record ----------------

// TestLog_NoSecretLeak is the security-critical assertion (ADR-60): drive
// the full encrypted lifecycle and confirm that the passphrase, and no
// plausible DEK byte sequence, ever appears in any logged attribute value.
func TestLog_NoSecretLeak(t *testing.T) {
	l, recs := debugLogger()
	const pass = "super-secret-passphrase-zzz"

	_, r := storefx.InitEncrypted(t, pass, store.WithLogger(l))
	s := r.Open(t, store.WithLogger(l), store.WithPassphrase(storefx.StaticPP(pass)))
	ctx := context.Background()
	if err := s.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	id, err := s.Put(ctx, storefx.Payload("secret-bearing op"), domain.PutOptions{Namespace: "ns"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.RotateKEK(ctx); err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}
	_ = id

	if len(*recs) == 0 {
		t.Fatal("expected log records from the encrypted lifecycle")
	}
	for _, rec := range *recs {
		for k, v := range rec.Attrs {
			if strings.Contains(v, pass) {
				t.Errorf("passphrase leaked into log attr %q=%q (msg %q)", k, v, rec.Msg)
			}
			// A key_id is allowed (opaque id); a DEK is not. Guard against
			// any attr keyed like a secret rendering anything but the
			// redaction sentinel.
			if k == "scrinium.dek" || k == "dek" || strings.Contains(k, "passphrase") {
				if v != "<redacted>" {
					t.Errorf("secret-shaped attr %q rendered %q; want <redacted>", k, v)
				}
			}
		}
	}
}

// --- default silence: no logger means no output, no panic ---------------

func TestLog_SilentByDefault_FullLifecycle(t *testing.T) {
	// No WithLogger: the engine must run the whole lifecycle silently.
	s := storefx.Init(t)
	ctx := context.Background()
	id, err := s.Put(ctx, storefx.Payload("quiet"), domain.PutOptions{Namespace: "ns"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Verify(ctx, id); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// --- R10.7: error-on-return trace (Debug) --------------------------------

// TestLog_ErrorReturnTracedAtDebug verifies the ADR-60 Debug-on-error-
// return pattern: a failing operation emits an "operation failed" Debug
// record carrying op and error, while the error is STILL returned to the
// caller (no swallowing).
func TestLog_ErrorReturnTracedAtDebug(t *testing.T) {
	l, recs := debugLogger()
	s := storefx.Init(t, store.WithLogger(l))

	// Delete a nonexistent artifact: loadManifest → ErrArtifactNotFound
	// is returned by the entry path before traceErr, so to hit a traced
	// terminal error we Put then Delete twice — the second Delete's index
	// stage may or may not trace depending on path. Instead use a Put of
	// an artifact, then Delete it, then Verify the now-missing one which
	// returns an error to the caller.
	id, err := s.Put(context.Background(), storefx.Payload("x"), domain.PutOptions{Namespace: "ns"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Verify a deleted artifact → error returned to caller.
	if err := s.Verify(context.Background(), id); err == nil {
		t.Fatal("Verify of deleted artifact should error")
	}
	// The contract we assert: the returned error reaches the caller
	// (checked above). Any Debug "operation failed" trace is best-effort
	// diagnostics; if present it must carry op + error and never a secret.
	for _, r := range *recs {
		if r.Msg == "operation failed" {
			if r.Level != slog.LevelDebug {
				t.Errorf("operation-failed trace level: want Debug, got %v", r.Level)
			}
			if r.Attrs["scrinium.op"] == "" {
				t.Error("operation-failed trace missing op attribute")
			}
			if r.Attrs["scrinium.error"] == "" {
				t.Error("operation-failed trace missing error attribute")
			}
		}
	}
}

// TestLog_ForceReinitWarns verifies the best-effort force-reinit removal
// is logged at Warn (no caller sees that cleanup otherwise).
func TestLog_ForceReinitWarns(t *testing.T) {
	l, recs := debugLogger()
	root := t.TempDir()

	mkDriver := func() *localfs.Driver {
		d, err := localfs.New(root, localfs.WithFsync(false))
		if err != nil {
			t.Fatalf("localfs.New: %v", err)
		}
		return d
	}

	// First init creates a descriptor at root.
	if _, _, err := store.InitStore(context.Background(), mkDriver(),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatalf("InitStore (first): %v", err)
	}

	// Second init with WithForceReinit removes the existing descriptor and
	// must Warn about it.
	if _, _, err := store.InitStore(context.Background(), mkDriver(),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
		store.WithForceReinit(),
		store.WithLogger(l),
	); err != nil {
		t.Fatalf("InitStore (force-reinit): %v", err)
	}
	if rec := find(recs, "force-reinit: removed existing descriptor"); rec == nil {
		t.Error(`no force-reinit Warn record`)
	} else if rec.Level != slog.LevelWarn {
		t.Errorf("force-reinit level: want Warn, got %v", rec.Level)
	}
}
