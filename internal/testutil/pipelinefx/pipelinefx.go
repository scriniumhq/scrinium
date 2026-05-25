// Package pipelinefx wires Stores with a blob-transform pipeline (zstd
// compression and/or aes-gcm encryption) for store-level tests, hiding
// the registry/stage boilerplate. The aes-gcm DEK is a fixed, non-secret
// test key — deterministic so blob-at-rest assertions are reproducible.
// Never use it for anything real.
package pipelinefx

import (
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/internal/testutil/storefx"
	"scrinium.dev/store"
	"scrinium.dev/store/pipeline"
	"scrinium.dev/store/pipeline/stage/aesgcm"
	scriniumzstd "scrinium.dev/store/pipeline/stage/zstd"
)

// DEK is the fixed 32-byte test data-encryption key used by the aes-gcm
// stage. Deterministic on purpose; NOT a secret. Exposed so a test can
// build a second registry with a different key (e.g. a wrong-key
// negative test) if needed.
func DEK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

// Stack builds a Target-storage Store whose blob pipeline runs the given
// algorithms in order, and returns it together with its on-disk root for
// at-rest inspection. Supported algos: "zstd", "aes-gcm". Pipelines are
// incompatible with inline storage, so the store uses Target layout (the
// storefx default).
//
// Example:
//
//	s, root := pipelinefx.Stack(t, "zstd", "aes-gcm")
func Stack(t *testing.T, algos ...string) (store.Store, string) {
	t.Helper()
	reg := pipeline.NewTransformerRegistry()
	for _, a := range algos {
		switch a {
		case "zstd":
			reg = reg.Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))
		case "aes-gcm":
			f, err := aesgcm.New(DEK())
			if err != nil {
				t.Fatalf("pipelinefx: aesgcm.New: %v", err)
			}
			reg = reg.Register("aes-gcm", f)
		default:
			t.Fatalf("pipelinefx: unknown algorithm %q", a)
		}
	}
	cfg := domain.StoreConfig{Pipeline: append([]string(nil), algos...)}
	return storefx.InitWithRoot(t, store.WithReadRegistry(reg), store.WithConfig(cfg))
}
