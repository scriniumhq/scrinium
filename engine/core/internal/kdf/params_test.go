package kdf_test

import (
	"bytes"
	"errors"
	"testing"

	"scrinium.dev/engine/core/internal/kdf"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

func TestDefault_PassesValidate(t *testing.T) {
	p := kdf.Default()
	if err := kdf.Validate(p); err != nil {
		t.Fatalf("Default() fails Validate: %v", err)
	}
}

func TestDefault_MatchesCryptographySpec(t *testing.T) {
	// Lock-in: changing these values requires bumping
	// Descriptor.SchemaVersion, because old Stores cannot
	// re-derive their KEK with new defaults.
	p := kdf.Default()
	if p.Time != 1 {
		t.Errorf("Time: got %d, want 1", p.Time)
	}
	if p.Memory != 65536 {
		t.Errorf("Memory: got %d, want 65536", p.Memory)
	}
	if p.Threads != 4 {
		t.Errorf("Threads: got %d, want 4", p.Threads)
	}
}

func TestNewSalt_LengthAndRandomness(t *testing.T) {
	s1, err := kdf.NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	if len(s1) != kdf.SaltLen {
		t.Fatalf("len: got %d, want %d", len(s1), kdf.SaltLen)
	}
	s2, _ := kdf.NewSalt()
	if bytes.Equal(s1, s2) {
		t.Fatal("two NewSalt calls returned identical bytes")
	}
}

func TestValidate_RejectsTimeBelowMin(t *testing.T) {
	p := kdf.Default()
	p.Time = 0
	if !errors.Is(kdf.Validate(p), errs.ErrInvalidKDFParams) {
		t.Fatalf("expected ErrInvalidKDFParams for time=0")
	}
}

func TestValidate_RejectsMemoryBelowMin(t *testing.T) {
	p := kdf.Default()
	p.Memory = kdf.MinMemory - 1
	if !errors.Is(kdf.Validate(p), errs.ErrInvalidKDFParams) {
		t.Fatalf("expected ErrInvalidKDFParams for memory=%d", p.Memory)
	}
}

func TestValidate_RejectsThreadsBelowMin(t *testing.T) {
	p := kdf.Default()
	p.Threads = 0
	if !errors.Is(kdf.Validate(p), errs.ErrInvalidKDFParams) {
		t.Fatalf("expected ErrInvalidKDFParams for threads=0")
	}
}

func TestValidate_AcceptsMinimumValues(t *testing.T) {
	p := domain.KDFParams{
		Time:    kdf.MinTime,
		Memory:  kdf.MinMemory,
		Threads: kdf.MinThreads,
	}
	if err := kdf.Validate(p); err != nil {
		t.Fatalf("Validate at minimums: %v", err)
	}
}

// Validate must not reject parameters above defaults — paranoid
// users may want to crank Memory or Time and that is healthy.
func TestValidate_AcceptsHigherThanDefault(t *testing.T) {
	p := domain.KDFParams{
		Time:    10,
		Memory:  256 * 1024, // 256 MiB
		Threads: 8,
	}
	if err := kdf.Validate(p); err != nil {
		t.Fatalf("Validate at strong settings: %v", err)
	}
}
