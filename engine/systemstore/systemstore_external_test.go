package systemstore

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/cas"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/driverfx"
)

// fakeCrypto is a Plain-store CryptoProvider: no DEK, no keyID, nil provider.
// DEKForWrite is never reached for ManifestCryptoPlain.
type fakeCrypto struct{}

func (fakeCrypto) DEKForWrite(config.ManifestCrypto) ([]byte, error) { return nil, nil }
func (fakeCrypto) WriteKeyID() string                                { return "" }
func (fakeCrypto) KeyProvider() domain.KeyProvider                   { return nil }

// fakeResolver records the refs OpenExternal/DeleteExternal are called with and
// hands back a canned payload, standing in for the store's headless data plane.
type fakeResolver struct {
	payload []byte
	opened  []domain.ManifestDigest
	deleted []domain.ManifestDigest
}

func (r *fakeResolver) OpenExternal(_ context.Context, ref domain.ManifestDigest) (domain.ReadHandle, error) {
	r.opened = append(r.opened, ref)
	return cas.NewInlinePayloadHandle(domain.Manifest{}, r.payload), nil
}

func (r *fakeResolver) DeleteExternal(_ context.Context, ref domain.ManifestDigest) error {
	r.deleted = append(r.deleted, ref)
	return nil
}

func newExternalFixture(t *testing.T, res ExternalResolver) Store {
	t.Helper()
	drv := driverfx.LocalFS(t)
	cfg := config.StoreConfig{ContentHasher: config.HashSHA256, ManifestCrypto: config.ManifestCryptoPlain}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(drv, artifactfx.Hashes(), cfg, storeX, fakeCrypto{}, res, log)
}

// A pointer artifact (ExternalRef set) round-trips through the systemstore: Put
// stores only the pointer envelope, Get resolves the external payload via the
// injected resolver, and Delete cascades the resolver's deletion (ADR-105) so
// the external blob never leaks.
func TestExternal_PointerRoundTripAndDeleteCascade(t *testing.T) {
	const ref = domain.ManifestDigest("sha256:deadbeefcafe")
	res := &fakeResolver{payload: []byte("SQLite format 3\x00EXTERNAL-DB-BYTES")}
	ss := newExternalFixture(t, res)
	ctx := context.Background()
	name := "config.agent.checkpoint.20260101T000000Z"

	if err := ss.Put(ctx, NamedArtifact{Name: name, ExternalRef: ref}); err != nil {
		t.Fatalf("Put pointer: %v", err)
	}

	// Get resolves through the external resolver and streams its payload.
	rh, err := ss.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, err := io.ReadAll(rh)
	rh.Close()
	if err != nil {
		t.Fatalf("read resolved payload: %v", err)
	}
	if string(body) != string(res.payload) {
		t.Errorf("resolved payload = %q, want the external bytes", body)
	}
	if len(res.opened) != 1 || res.opened[0] != ref {
		t.Errorf("OpenExternal calls = %v, want exactly one with %q", res.opened, ref)
	}

	// Delete cascades to the external payload, then removes the pointer.
	if err := ss.Delete(ctx, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(res.deleted) != 1 || res.deleted[0] != ref {
		t.Errorf("DeleteExternal calls = %v, want exactly one with %q", res.deleted, ref)
	}
	if _, err := ss.Get(ctx, name); err == nil {
		t.Error("Get after Delete should fail — the pointer was removed")
	}
}

// An inline artifact (no ExternalRef) never touches the resolver: Put stores the
// payload in the envelope, Get returns it directly, and Delete does not cascade.
func TestExternal_InlineArtifactBypassesResolver(t *testing.T) {
	res := &fakeResolver{payload: []byte("UNUSED")}
	ss := newExternalFixture(t, res)
	ctx := context.Background()
	name := "scrub/cursor"

	if err := ss.Put(ctx, NamedArtifact{Name: name, Payload: strings.NewReader("inline-bytes")}); err != nil {
		t.Fatalf("Put inline: %v", err)
	}
	rh, err := ss.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, _ := io.ReadAll(rh)
	rh.Close()
	if string(body) != "inline-bytes" {
		t.Errorf("inline payload = %q, want %q", body, "inline-bytes")
	}
	if err := ss.Delete(ctx, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(res.opened) != 0 || len(res.deleted) != 0 {
		t.Errorf("resolver touched for an inline artifact: opened=%v deleted=%v", res.opened, res.deleted)
	}
}
