package wrapper_test

import (
	"testing"

	"scrinium.dev/engine/wrapper"
)

// TestValidate covers the Rules Engine (ADR-75): closed structural set,
// ≤1 of each structural, forced chunker → bundler order, chunker-not-on-
// Backup, and the size invariant. stack is apply order (innermost first),
// so the outermost chunker sits at a HIGHER index than the bundler.
func TestValidate(t *testing.T) {
	d := func(name string, c wrapper.Class) wrapper.Descriptor {
		return wrapper.Descriptor{Name: name, Class: c}
	}
	chunker := d("chunker", wrapper.Structural)
	bundler := d("bundler", wrapper.Structural)
	audit := d("audit", wrapper.Behavioral)

	cases := []struct {
		name    string
		stack   []wrapper.Descriptor
		opts    wrapper.ValidateOptions
		wantErr bool
	}{
		{"empty", nil, wrapper.ValidateOptions{}, false},
		{"single behavioral", []wrapper.Descriptor{audit}, wrapper.ValidateOptions{}, false},
		{"two behavioral order-free", []wrapper.Descriptor{audit, d("metrics", wrapper.Behavioral)}, wrapper.ValidateOptions{}, false},
		{"single chunker", []wrapper.Descriptor{chunker}, wrapper.ValidateOptions{}, false},
		{"single bundler", []wrapper.Descriptor{bundler}, wrapper.ValidateOptions{}, false},
		{"correct order: bundler inner, chunker outer", []wrapper.Descriptor{bundler, chunker}, wrapper.ValidateOptions{}, false},
		{"wrong order: chunker inner, bundler outer", []wrapper.Descriptor{chunker, bundler}, wrapper.ValidateOptions{}, true},
		{"duplicate chunker", []wrapper.Descriptor{chunker, chunker}, wrapper.ValidateOptions{}, true},
		{"duplicate bundler", []wrapper.Descriptor{bundler, bundler}, wrapper.ValidateOptions{}, true},
		{"structural outside closed set", []wrapper.Descriptor{d("compressor", wrapper.Structural)}, wrapper.ValidateOptions{}, true},
		{"behavioral name is unconstrained", []wrapper.Descriptor{d("compressor", wrapper.Behavioral)}, wrapper.ValidateOptions{}, false},
		{"behavioral mixed among structural", []wrapper.Descriptor{audit, bundler, chunker}, wrapper.ValidateOptions{}, false},
		{"chunker forbidden on backup", []wrapper.Descriptor{chunker}, wrapper.ValidateOptions{OnBackup: true}, true},
		{"bundler allowed on backup", []wrapper.Descriptor{bundler}, wrapper.ValidateOptions{OnBackup: true}, false},
		{"size invariant valid", []wrapper.Descriptor{chunker}, wrapper.ValidateOptions{MaxChunkSize: 64, DirectWriteThreshold: 128, MaxBundleSize: 256}, false},
		{"size invariant equal bounds", []wrapper.Descriptor{chunker}, wrapper.ValidateOptions{MaxChunkSize: 128, DirectWriteThreshold: 128, MaxBundleSize: 128}, false},
		{"size invariant chunk > threshold", []wrapper.Descriptor{chunker}, wrapper.ValidateOptions{MaxChunkSize: 256, DirectWriteThreshold: 128, MaxBundleSize: 512}, true},
		{"size invariant threshold > bundle", []wrapper.Descriptor{chunker}, wrapper.ValidateOptions{MaxChunkSize: 64, DirectWriteThreshold: 512, MaxBundleSize: 256}, true},
		{"size invariant partial is skipped", []wrapper.Descriptor{chunker}, wrapper.ValidateOptions{MaxChunkSize: 64, MaxBundleSize: 256}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := wrapper.Validate(tc.stack, tc.opts)
			if tc.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}
