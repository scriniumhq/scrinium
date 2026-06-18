package crypto

import (
	"bytes"
	"testing"
)

// allZero reports whether every byte of b is zero. Used to assert that
// secret material is scrubbed, not merely dropped.
func allZero(b []byte) bool {
	return bytes.Equal(b, make([]byte, len(b)))
}

// TestCloseSecrets_WipesAndClearsDEK is the unit-level counterpart of the
// store's TestClose_WipesDEK: it verifies CloseSecrets both zeroes the
// backing array (so the key does not linger in freed memory) and clears
// the field. The store test only asserts the observable presence bit; the
// byte-level scrub is checked here, where the field is reachable.
func TestCloseSecrets_WipesAndClearsDEK(t *testing.T) {
	s := New(nil, []byte{1, 2, 3, 4, 5, 6, 7, 8}, nil, nil, nil, nil)
	ref := s.dek // same backing array; observe its bytes after the wipe

	resolver := s.CloseSecrets()
	if resolver != nil {
		t.Errorf("CloseSecrets resolver: want nil (none installed), got %v", resolver)
	}
	if s.dek != nil {
		t.Errorf("dek: want nil after CloseSecrets, got %v", s.dek)
	}
	if !allZero(ref) {
		t.Errorf("dek backing array: want zeroed after CloseSecrets, got %v", ref)
	}
}

// TestWipeDEK_WipesAndClearsDEK covers the unlock-failure path's WipeDEK
// the same way.
func TestWipeDEK_WipesAndClearsDEK(t *testing.T) {
	s := New(nil, []byte{9, 8, 7, 6}, nil, nil, nil, nil)
	ref := s.dek

	s.WipeDEK()
	if s.dek != nil {
		t.Errorf("dek: want nil after WipeDEK, got %v", s.dek)
	}
	if !allZero(ref) {
		t.Errorf("dek backing array: want zeroed after WipeDEK, got %v", ref)
	}
}

// TestCloseSecrets_NilDEK_NoPanic guards the empty/locked-Store path.
func TestCloseSecrets_NilDEK_NoPanic(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, nil)
	if r := s.CloseSecrets(); r != nil {
		t.Errorf("resolver: want nil, got %v", r)
	}
	if s.dek != nil {
		t.Errorf("dek: want nil, got %v", s.dek)
	}
}

// TestHasDEK reports presence, never material.
func TestHasDEK(t *testing.T) {
	if got := New(nil, []byte{1}, nil, nil, nil, nil).HasDEK(); !got {
		t.Error("HasDEK with a DEK: want true")
	}
	if got := New(nil, nil, nil, nil, nil, nil).HasDEK(); got {
		t.Error("HasDEK with nil DEK: want false")
	}
	if got := New(nil, []byte{}, nil, nil, nil, nil).HasDEK(); got {
		t.Error("HasDEK with empty DEK: want false")
	}
}
