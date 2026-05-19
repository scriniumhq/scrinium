package core

import (
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
)

// shouldVerifyOnRead resolves the per-Get verification decision
// from the active policy, the artifact's pipeline composition,
// and the driver's capabilities.
//
// ForceEnabled and Disabled are explicit overrides. Auto consults
// the artifact and the medium: when the pipeline includes an AEAD
// plugin (its authentication tag already detects tampering on
// every read) or the driver reports CapNativeChecksum (the medium
// catches silent bit rot), the explicit ContentHash recomputation
// is redundant and skipped.
//
// Empty pipeline + driver without CapNativeChecksum + Auto = on:
// a plain on-disk blob on commodity media needs the explicit
// check.
//
// Unknown algorithms in the pipeline are treated as non-AEAD
// (the registry returns an error and the loop continues); the
// conservative default keeps verification on for stages whose
// integrity guarantees the engine cannot prove statically.
//
// transformers may be nil. In that case Auto falls through to
// true regardless of pipeline contents — AEAD detection requires
// the registry. The Get-path always passes s.transformers; the
// nil branch exists for isolated unit tests and defensive wiring.
//
// The empty-string policy is treated as Auto. The engine's
// config_default.go promotes the zero value to Auto before the
// active config is read, so this branch only fires for callers
// that bypass config (none today; the defensive handling is
// cheap).
func shouldVerifyOnRead(
	policy domain.VerifyOnReadPolicy,
	stages []domain.PipelineStage,
	caps driver.CapabilityMask,
	transformers TransformerRegistry,
) bool {
	switch policy {
	case domain.VerifyOnReadForceEnabled:
		return true
	case domain.VerifyOnReadDisabled:
		return false
	}
	// Auto (or unset — treated as Auto).
	if caps.Has(driver.CapNativeChecksum) {
		return false
	}
	if transformers == nil {
		return true
	}
	for _, s := range stages {
		f, err := transformers.Get(s.Algorithm)
		if err != nil {
			continue
		}
		if _, ok := f.(AEADCapable); ok {
			return false
		}
	}
	return true
}
