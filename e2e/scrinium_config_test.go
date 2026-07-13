//go:build e2e

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	scrinium "scrinium.dev"
	"scrinium.dev/config"
	"scrinium.dev/errs"
)

// Consolidated config e2e through the facade (ADR-110 branch): the
// declarative file's whole life — from seeding to class refusals — on
// the public API, the exact path an operator walks. The engine level
// is covered in engine/store (connection_e2e, config_freshness_e2e);
// this file pins the facade seams: LoadInitYAML/LoadYAML, strict
// decode, UpdateConfig versioning and rollback.

func configYAML(root string, retention string) []byte {
	return []byte(fmt.Sprintf(`store:
  driver: file://%s
  policy:
    deletionPolicy: retention
    retention: %s
`, root, retention))
}

// TestConfigE2E_DeclarativeSeedAndReopen: the file seeds class II at
// creation; reopening with the same file passes; with an edited
// retention — a governance refusal (the former silent no-op is loud now).
func TestConfigE2E_DeclarativeSeedAndReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Seed.
	c, err := scrinium.LoadInitYAML(ctx, configYAML(root, "72h"))
	if err != nil {
		t.Fatalf("LoadInitYAML: %v", err)
	}
	if got := c.Store.Config().DeletionPolicy; got != config.DeletionPolicyRetention {
		t.Errorf("seeded DeletionPolicy = %q, want Retention", got)
	}
	if got := c.Store.Config().RetentionPeriod; got != 72*time.Hour {
		t.Errorf("seeded RetentionPeriod = %v, want 72h", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Same file — same connection: passes.
	c, err = scrinium.LoadYAML(ctx, configYAML(root, "72h"))
	if err != nil {
		t.Fatalf("LoadYAML (matching): %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// An edited retention in the file is refused with the admin path
	// named — not silently ignored (review finding N-1, INV-110-5).
	_, err = scrinium.LoadYAML(ctx, configYAML(root, "24h"))
	if !errors.Is(err, errs.ErrGovernanceMismatch) {
		t.Fatalf("edited retention must refuse with ErrGovernanceMismatch, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "UpdateConfig") {
		t.Errorf("refusal must name the admin path, got %q", err.Error())
	}
}

// TestConfigE2E_UpdateVersioningAndRollback: a class-II change through
// the admin path is versioned (history newest-first); a rollback is a
// NEW max-seq copy of the previous values, not a pointer rewind.
func TestConfigE2E_UpdateVersioningAndRollback(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	c, err := scrinium.LoadInitYAML(ctx, configYAML(root, "72h"))
	if err != nil {
		t.Fatalf("LoadInitYAML: %v", err)
	}
	defer func() { _ = c.Close() }()

	hist, err := c.Store.ConfigHistory(ctx)
	if err != nil {
		t.Fatalf("ConfigHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("fresh store: history has %d versions, want 1", len(hist))
	}

	// The admin tightens retention.
	req := c.Store.Config()
	req.RetentionPeriod = 240 * time.Hour
	if err := c.Store.UpdateConfig(ctx, req); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	hist, _ = c.Store.ConfigHistory(ctx)
	if len(hist) != 2 || hist[0].RetentionPeriod != 240*time.Hour {
		t.Fatalf("after update: len=%d, hist[0].Retention=%v; want 2 and 240h",
			len(hist), hist[0].RetentionPeriod)
	}
	// The previous version stays alive in the history.
	if hist[1].RetentionPeriod != 72*time.Hour {
		t.Errorf("hist[1].Retention = %v, want the original 72h", hist[1].RetentionPeriod)
	}

	// Rollback = publishing the previous values as a new version
	// (ADR-85): the history grows, a copy of the old becomes active.
	rollback := hist[1]
	if err := c.Store.UpdateConfig(ctx, rollback); err != nil {
		t.Fatalf("UpdateConfig (rollback): %v", err)
	}
	hist, _ = c.Store.ConfigHistory(ctx)
	if len(hist) != 3 {
		t.Fatalf("rollback must append, not rewind: len=%d, want 3", len(hist))
	}
	if hist[0].RetentionPeriod != 72*time.Hour {
		t.Errorf("active after rollback = %v, want 72h", hist[0].RetentionPeriod)
	}
	if got := c.Store.Config().RetentionPeriod; got != 72*time.Hour {
		t.Errorf("Config() after rollback = %v, want 72h", got)
	}
}

// TestConfigE2E_StrictDecodeThroughFacade: a typo and a removed key
// fail at facade parse time, before any I/O.
func TestConfigE2E_StrictDecodeThroughFacade(t *testing.T) {
	ctx := context.Background()

	typo := []byte("store:\n  driver: file:///nowhere\n  policy:\n    retenton: 30d\n")
	if _, err := scrinium.LoadInitYAML(ctx, typo); err == nil {
		t.Fatal("typo key must fail strict decode through the facade")
	}

	removed := []byte("store:\n  driver: file:///nowhere\n  policy:\n    scrub:\n      perStageVerification: true\n")
	if _, err := scrinium.LoadInitYAML(ctx, removed); err == nil {
		t.Fatal("removed key must fail strict decode through the facade")
	}
}

// TestConfigE2E_MaxArtifactSizeEnforced: the maxArtifactSize key seeds
// the class-II limit and the limit actually bites on Put — the file
// stops being a dead key with an illusion of enforcement.
func TestConfigE2E_MaxArtifactSizeEnforced(t *testing.T) {
	ctx := context.Background()
	doc := []byte(fmt.Sprintf(`store:
  driver: file://%s
  policy:
    maxArtifactSize: 1KB
`, t.TempDir()))
	c, err := scrinium.LoadInitYAML(ctx, doc)
	if err != nil {
		t.Fatalf("LoadInitYAML: %v", err)
	}
	defer func() { _ = c.Close() }()

	if got := c.Store.Config().MaxArtifactSize; got != 1000 {
		t.Fatalf("seeded MaxArtifactSize = %d, want 1000 (1KB, decimal)", got)
	}
	if _, err := c.Put(ctx, scrinium.Artifact{Payload: strings.NewReader(strings.Repeat("x", 2000))}); !errors.Is(err, errs.ErrArtifactTooLarge) {
		t.Fatalf("Put over the declarative limit: want ErrArtifactTooLarge, got %v", err)
	}
	if _, err := c.Put(ctx, scrinium.Artifact{Payload: strings.NewReader("tiny")}); err != nil {
		t.Fatalf("Put under the limit must pass, got %v", err)
	}
}
