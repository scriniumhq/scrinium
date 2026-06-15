package chunker_test

import (
	"errors"
	"testing"

	"scrinium.dev/engine/wrapper"
	"scrinium.dev/errs"
	"scrinium.dev/x/chunker"
)

// TestChunker_RegisteredWithDescriptor checks the blank-import registration
// (init) and the Descriptor the Rules Engine reads: chunker is Structural.
func TestChunker_RegisteredWithDescriptor(t *testing.T) {
	f, ok := wrapper.Lookup("chunker")
	if !ok {
		t.Fatal(`wrapper.Lookup("chunker") = false; init registration missing`)
	}
	if d := f.Descriptor(); d.Name != "chunker" || d.Class != wrapper.Structural {
		t.Fatalf("Descriptor = %+v, want {chunker structural}", d)
	}
}

// TestChunker_WrapNotImplemented documents the M4.5 placeholder: the
// contract is in place but the decorator itself lands later.
func TestChunker_WrapNotImplemented(t *testing.T) {
	_, err := chunker.New(chunker.Config{}).Wrap(nil, wrapper.Deps{})
	if !errors.Is(err, errs.ErrNotImplemented) {
		t.Fatalf("Wrap() error = %v, want ErrNotImplemented", err)
	}
}
