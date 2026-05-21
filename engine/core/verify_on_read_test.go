package core

import (
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
)

// fakePlainFactory is a TransformerFactory that does NOT
// implement AEADCapable — stands in for a compressor like zstd.
type fakePlainFactory struct{}

func (fakePlainFactory) NewEncoder(ctx EncodeContext) Encoder          { panic("unused") }
func (fakePlainFactory) NewDecoder(stage domain.PipelineStage) Decoder { panic("unused") }

// fakeAEADFactory implements AEADCapable — stands in for aesgcm.
type fakeAEADFactory struct{}

func (fakeAEADFactory) NewEncoder(ctx EncodeContext) Encoder          { panic("unused") }
func (fakeAEADFactory) NewDecoder(stage domain.PipelineStage) Decoder { panic("unused") }
func (fakeAEADFactory) AEAD()                                         {}

func newTestRegistry(t *testing.T) TransformerRegistry {
	t.Helper()
	return NewTransformerRegistry().
		Register("zstd", fakePlainFactory{}).
		Register("aes-gcm", fakeAEADFactory{})
}

// --- Policy overrides ---

func TestShouldVerifyOnRead_ForceEnabled_AlwaysTrue(t *testing.T) {
	// Even when every Auto signal points to "off", ForceEnabled
	// must verify.
	got := shouldVerifyOnRead(
		domain.VerifyOnReadForceEnabled,
		[]domain.PipelineStage{{Algorithm: "aes-gcm"}},
		driver.CapNativeChecksum,
		newTestRegistry(t),
	)
	if !got {
		t.Error("ForceEnabled must always verify")
	}
}

func TestShouldVerifyOnRead_Disabled_AlwaysFalse(t *testing.T) {
	// Even with no protection at all (plain pipeline, plain
	// driver), Disabled skips verification.
	got := shouldVerifyOnRead(
		domain.VerifyOnReadDisabled,
		[]domain.PipelineStage{{Algorithm: "zstd"}},
		0,
		newTestRegistry(t),
	)
	if got {
		t.Error("Disabled must never verify")
	}
}

// --- Auto branch ---

func TestShouldVerifyOnRead_Auto_NativeChecksum_Off(t *testing.T) {
	got := shouldVerifyOnRead(
		domain.VerifyOnReadAuto,
		nil,
		driver.CapNativeChecksum,
		newTestRegistry(t),
	)
	if got {
		t.Error("Auto + CapNativeChecksum must not verify")
	}
}

func TestShouldVerifyOnRead_Auto_AEADStage_Off(t *testing.T) {
	got := shouldVerifyOnRead(
		domain.VerifyOnReadAuto,
		[]domain.PipelineStage{
			{Algorithm: "zstd"},
			{Algorithm: "aes-gcm"},
		},
		0,
		newTestRegistry(t),
	)
	if got {
		t.Error("Auto + AEAD stage must not verify")
	}
}

func TestShouldVerifyOnRead_Auto_PlainPipeline_On(t *testing.T) {
	got := shouldVerifyOnRead(
		domain.VerifyOnReadAuto,
		[]domain.PipelineStage{{Algorithm: "zstd"}},
		0,
		newTestRegistry(t),
	)
	if !got {
		t.Error("Auto + non-AEAD pipeline + no native checksum must verify")
	}
}

func TestShouldVerifyOnRead_Auto_EmptyPipeline_On(t *testing.T) {
	got := shouldVerifyOnRead(
		domain.VerifyOnReadAuto,
		nil,
		0,
		newTestRegistry(t),
	)
	if !got {
		t.Error("Auto + empty pipeline + no native checksum must verify")
	}
}

// --- Edge cases ---

func TestShouldVerifyOnRead_Auto_UnknownAlgorithm_Verifies(t *testing.T) {
	// Stage refers to an algorithm not in the registry. The
	// registry returns an error; the loop treats the stage as
	// non-AEAD and continues. With no AEAD stage found and no
	// CapNativeChecksum, Auto returns true.
	got := shouldVerifyOnRead(
		domain.VerifyOnReadAuto,
		[]domain.PipelineStage{{Algorithm: "unknown-xyz"}},
		0,
		newTestRegistry(t),
	)
	if !got {
		t.Error("Auto + unknown algorithm + no native checksum must verify")
	}
}

func TestShouldVerifyOnRead_Auto_NilRegistry_Verifies(t *testing.T) {
	// Without a registry the engine cannot detect AEAD stages,
	// so Auto falls through to true (verify).
	got := shouldVerifyOnRead(
		domain.VerifyOnReadAuto,
		[]domain.PipelineStage{{Algorithm: "aes-gcm"}},
		0,
		nil,
	)
	if !got {
		t.Error("nil registry must fall through to verify")
	}
}

func TestShouldVerifyOnRead_Auto_NativeChecksum_ShortCircuitsAEADCheck(t *testing.T) {
	// CapNativeChecksum is checked before the AEAD scan; the
	// registry is not consulted at all. We do not have an easy
	// hook to assert "registry not touched", but covering the
	// happy path here documents the precedence in tests.
	got := shouldVerifyOnRead(
		domain.VerifyOnReadAuto,
		[]domain.PipelineStage{{Algorithm: "zstd"}},
		driver.CapNativeChecksum,
		newTestRegistry(t),
	)
	if got {
		t.Error("CapNativeChecksum must short-circuit Auto to off")
	}
}

func TestShouldVerifyOnRead_UnsetPolicy_TreatedAsAuto(t *testing.T) {
	// VerifyOnReadPolicy("") is the zero value. config_default
	// normally promotes it to Auto before this helper runs;
	// shouldVerifyOnRead is defensive and treats "" identically.
	got := shouldVerifyOnRead(
		"",
		nil,
		0,
		newTestRegistry(t),
	)
	if !got {
		t.Error("unset policy must behave like Auto (verify by default)")
	}
}
