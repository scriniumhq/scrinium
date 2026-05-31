package sqlite

import (
	"errors"
	"testing"

	"scrinium.dev/errs"
)

// IndexManifest / DeleteManifest / ManifestExists
// behaviour lives in the conformance suite at
// internal/testutil/indextest. This file is for sqlite-specific
// behaviour only — currently the classifyError mapping from
// SQLite-driver error strings into errs.* sentinels.

func TestClassifyError_Nil(t *testing.T) {
	if err := classifyError(nil); err != nil {
		t.Errorf("classifyError(nil) = %v, want nil", err)
	}
}

func TestClassifyError_BusyMaps(t *testing.T) {
	err := classifyError(errors.New("database is locked"))
	if !errors.Is(err, errs.ErrLeaseHeld) {
		t.Errorf("expected errs.ErrLeaseHeld, got %v", err)
	}
}

func TestClassifyError_PassThrough(t *testing.T) {
	orig := errors.New("some other error")
	if err := classifyError(orig); err != orig {
		t.Errorf("non-busy error should pass through unchanged")
	}
}
