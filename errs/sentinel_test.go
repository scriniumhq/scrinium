package errs_test

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"scrinium.dev/errs"
)

// TestBridgedSentinels_IdentityStillMatches verifies that adding
// bridge edges does not break errors.Is against the sentinel
// itself — the foundational invariant for every caller that
// already does errors.Is(err, errs.ErrFoo).
func TestBridgedSentinels_IdentityStillMatches(t *testing.T) {
	cases := []error{
		errs.ErrPathNotFound,
		errs.ErrPathExists,
		errs.ErrInvalidPath,
		errs.ErrIsADirectory,
		errs.ErrNotADirectory,
		errs.ErrNotEmpty,
		errs.ErrEditingDisabled,
		errs.ErrScratchQuota,
		errs.ErrArtifactNotFound,
		errs.ErrStoreNotFound,
		errs.ErrStoreAlreadyExists,
		errs.ErrStoreReadOnly,
	}
	for _, sent := range cases {
		t.Run(sent.Error(), func(t *testing.T) {
			wrapped := fmt.Errorf("layer: %w", sent)
			if !errors.Is(wrapped, sent) {
				t.Errorf("errors.Is(wrapped, %v) = false; want true", sent)
			}
		})
	}
}

// TestBridgedSentinels_BridgeMatches verifies the new behaviour:
// host code can errors.Is against an io/fs sentinel and have it
// match the corresponding scrinium sentinel without knowing
// scrinium specifics.
func TestBridgedSentinels_BridgeMatches(t *testing.T) {
	cases := []struct {
		name   string
		sent   error
		bridge error
	}{
		{"ErrPathNotFound→fs.ErrNotExist", errs.ErrPathNotFound, fs.ErrNotExist},
		{"ErrArtifactNotFound→fs.ErrNotExist", errs.ErrArtifactNotFound, fs.ErrNotExist},
		{"ErrStoreNotFound→fs.ErrNotExist", errs.ErrStoreNotFound, fs.ErrNotExist},

		{"ErrPathExists→fs.ErrExist", errs.ErrPathExists, fs.ErrExist},
		{"ErrStoreAlreadyExists→fs.ErrExist", errs.ErrStoreAlreadyExists, fs.ErrExist},

		{"ErrInvalidPath→fs.ErrInvalid", errs.ErrInvalidPath, fs.ErrInvalid},
		{"ErrIsADirectory→fs.ErrInvalid", errs.ErrIsADirectory, fs.ErrInvalid},
		{"ErrNotADirectory→fs.ErrInvalid", errs.ErrNotADirectory, fs.ErrInvalid},
		{"ErrNotEmpty→fs.ErrInvalid", errs.ErrNotEmpty, fs.ErrInvalid},

		{"ErrEditingDisabled→fs.ErrPermission", errs.ErrEditingDisabled, fs.ErrPermission},
		{"ErrScratchQuota→fs.ErrPermission", errs.ErrScratchQuota, fs.ErrPermission},
		{"ErrStoreReadOnly→fs.ErrPermission", errs.ErrStoreReadOnly, fs.ErrPermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Direct bridge.
			if !errors.Is(tc.sent, tc.bridge) {
				t.Errorf("errors.Is(%v, %v) = false; want true",
					tc.sent, tc.bridge)
			}
			// Bridge through wrapping (the typical host path).
			wrapped := fmt.Errorf("layer-1: %w",
				fmt.Errorf("layer-2: %w", tc.sent))
			if !errors.Is(wrapped, tc.bridge) {
				t.Errorf("errors.Is(wrapped, %v) = false; want true",
					tc.bridge)
			}
		})
	}
}

// TestBridgedSentinels_NoFalseMatches verifies that bridges are
// limited to declared targets — a bridged sentinel does NOT match
// every io/fs error.
func TestBridgedSentinels_NoFalseMatches(t *testing.T) {
	// ErrPathNotFound bridges to fs.ErrNotExist; it must NOT bridge
	// to fs.ErrPermission or fs.ErrInvalid.
	if errors.Is(errs.ErrPathNotFound, fs.ErrPermission) {
		t.Error("ErrPathNotFound unexpectedly bridges to fs.ErrPermission")
	}
	if errors.Is(errs.ErrPathNotFound, fs.ErrInvalid) {
		t.Error("ErrPathNotFound unexpectedly bridges to fs.ErrInvalid")
	}
	// And vice versa: ErrEditingDisabled bridges to fs.ErrPermission,
	// not to fs.ErrNotExist.
	if errors.Is(errs.ErrEditingDisabled, fs.ErrNotExist) {
		t.Error("ErrEditingDisabled unexpectedly bridges to fs.ErrNotExist")
	}
}
