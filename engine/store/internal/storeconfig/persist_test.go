package storeconfig

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"testing"
	"time"

	"crypto/sha256"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/internal/named"
	"scrinium.dev/errs"
)

// White-box unit tests of the system/config persistence path. Under the
// seq model (ADR-85) the active config is the highest system/config
// version and there is no pointer file, so there are no
// corrupted-pointer / dangling-pointer cases. Write is self-contained
// (driver + hash registry, no ArtifactWriter), so the round-trip lives
// here rather than on the core side.

// testHashes is a minimal sha256-only domain.HashRegistry. Defined
// locally rather than reusing storefx.Hashes() because storefx imports
// engine/core, and core imports this package — pulling storefx in would
// create an import cycle in the test binary.
type testHashes struct{}

func (testHashes) Parse(h string) (string, []byte, error) {
	i := strings.IndexByte(h, '-')
	if i <= 0 {
		return "", nil, fmt.Errorf("malformed hash id %q", h)
	}
	raw, err := hex.DecodeString(h[i+1:])
	if err != nil {
		return "", nil, err
	}
	return h[:i], raw, nil
}

func (testHashes) NewHasher(algo string) (hash.Hash, error) {
	if algo == "sha256" {
		return sha256.New(), nil
	}
	return nil, fmt.Errorf("unknown algo %q", algo)
}

func (testHashes) Format(algo string, raw []byte) string {
	return algo + "-" + hex.EncodeToString(raw)
}

func (h testHashes) Register(string, func() hash.Hash) domain.HashRegistry { return h }

func newDriver(t *testing.T) *localfs.Driver {
	t.Helper()
	drv, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	return drv
}

// sampleConfig is a fully-specified Plain config; ContentHasher must be
// set so BuildInlineManifest can resolve a hasher.
func sampleConfig() domain.StoreConfig {
	return domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashSHA256,
		ManifestEncoding: domain.ManifestEncodingJSON,
		ManifestCrypto:   domain.ManifestCryptoPlain,
		RetentionPeriod:  3 * time.Hour,
	}
}

func TestRead_Missing(t *testing.T) {
	drv := newDriver(t)
	_, err := Read(context.Background(), drv, testHashes{})
	if !errors.Is(err, errs.ErrConfigMissing) {
		t.Fatalf("expected ErrConfigMissing, got %v", err)
	}
}

func TestWriteRead_RoundTrip(t *testing.T) {
	drv := newDriver(t)
	cfg := sampleConfig()
	if err := Write(context.Background(), drv, testHashes{}, cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(context.Background(), drv, testHashes{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.PathTopology != cfg.PathTopology {
		t.Errorf("PathTopology: got %q, want %q", got.PathTopology, cfg.PathTopology)
	}
	if got.RetentionPeriod != cfg.RetentionPeriod {
		t.Errorf("RetentionPeriod: got %v, want %v", got.RetentionPeriod, cfg.RetentionPeriod)
	}
}

// TestHistory_NewestFirst writes three versions and checks History
// returns them newest-first, with Read (the active config) equal to the
// highest version.
func TestHistory_NewestFirst(t *testing.T) {
	drv := newDriver(t)
	cfg := sampleConfig()

	for _, d := range []time.Duration{1 * time.Hour, 2 * time.Hour, 4 * time.Hour} {
		cfg.RetentionPeriod = d
		if err := Write(context.Background(), drv, testHashes{}, cfg); err != nil {
			t.Fatalf("Write %v: %v", d, err)
		}
	}

	hist, err := History(context.Background(), drv, testHashes{})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("len: got %d, want 3", len(hist))
	}
	if hist[0].RetentionPeriod != 4*time.Hour {
		t.Errorf("hist[0]: got %v, want 4h (newest)", hist[0].RetentionPeriod)
	}
	if hist[len(hist)-1].RetentionPeriod != 1*time.Hour {
		t.Errorf("hist[last]: got %v, want 1h (oldest)", hist[len(hist)-1].RetentionPeriod)
	}

	active, err := Read(context.Background(), drv, testHashes{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if active.RetentionPeriod != 4*time.Hour {
		t.Errorf("active: got %v, want 4h", active.RetentionPeriod)
	}
}

// readVersionFile returns the raw on-disk bytes of a system/config
// version, so a test can corrupt them and check the read path reacts.
func readVersionFile(t *testing.T, drv *localfs.Driver, path string) []byte {
	t.Helper()
	rc, err := drv.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("Get %q: %v", path, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll %q: %v", path, err)
	}
	return data
}

// TestRead_RejectsTamperedVersion verifies a config version whose on-disk
// bytes were altered is never returned as a clean config: Read delegates
// to named.Load, whose verify-on-read rejects it. The precise error
// depends on where the flipped byte lands (a header byte fails the parse;
// a payload byte fails the content hash with ErrCorruptedContent), so the
// contract under test is rejection, not a specific sentinel.
func TestRead_RejectsTamperedVersion(t *testing.T) {
	drv := newDriver(t)
	if err := Write(context.Background(), drv, testHashes{}, sampleConfig()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	seq, found, err := named.ResolveActiveSeq(context.Background(), drv, configName)
	if err != nil || !found {
		t.Fatalf("resolve active: err=%v found=%v", err, found)
	}
	path, err := named.VersionPath(configName, seq)
	if err != nil {
		t.Fatalf("VersionPath: %v", err)
	}

	raw := readVersionFile(t, drv, path)
	if len(raw) < 8 {
		t.Fatalf("version file improbably short: %d bytes", len(raw))
	}
	tampered := append([]byte{}, raw...)
	tampered[len(tampered)/2] ^= 0xFF // flip a mid-file byte
	if err := drv.Put(context.Background(), path, bytes.NewReader(tampered)); err != nil {
		t.Fatalf("Put tampered: %v", err)
	}

	if _, err := Read(context.Background(), drv, testHashes{}); err == nil {
		t.Fatal("Read returned a tampered config as clean; corruption must surface as an error")
	}
}

// TestHistory_EmptyStore_ReturnsEmpty pins the Read/History divergence on
// a fresh store: Read reports ErrConfigMissing, but History tolerates the
// empty version set and returns an empty slice with no error.
func TestHistory_EmptyStore_ReturnsEmpty(t *testing.T) {
	drv := newDriver(t)
	hist, err := History(context.Background(), drv, testHashes{})
	if err != nil {
		t.Fatalf("History on empty store: unexpected error %v", err)
	}
	if len(hist) != 0 {
		t.Errorf("History on empty store: got %d entries, want 0", len(hist))
	}
}

// R-a (config review): KDFParams are input-only at InitStore and live
// in the descriptor; Write must never serialise them into a
// store.config snapshot (they used to leak into every version).
func TestWrite_StripsKDFParams(t *testing.T) {
	drv := newDriver(t)
	cfg := sampleConfig()
	cfg.KDFParams = &domain.KDFParams{Time: 3, Memory: 64 * 1024, Threads: 4}

	if err := Write(context.Background(), drv, testHashes{}, cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The caller's value is untouched (Write gets a copy).
	if cfg.KDFParams == nil {
		t.Fatal("caller's KDFParams must not be mutated")
	}
	got, err := Read(context.Background(), drv, testHashes{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.KDFParams != nil {
		t.Errorf("KDFParams leaked into the store.config snapshot: %+v", got.KDFParams)
	}
}
