package store

// Test-only exports of package-private symbols. Lives in
// export_test.go so the alias surface is invisible outside test
// builds. Future system writers (M3 Scrub/Snapshot/Maintenance)
// extend this file with their own write/read helper aliases.

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"scrinium.dev/engine/pipeline"
)

// StoreKeyResolver exposes the internal keyResolver field for
// tests so they can assert that promoteKeyResolverIfDefault
// did or did not run. Returns nil for non-*store implementers
// (e.g. test mocks) so the helper degrades cleanly.
func StoreKeyResolver(s Store) pipeline.KeyResolver {
	concrete, ok := s.(*store)
	if !ok {
		return nil
	}
	return concrete.dataFacet.core.crypto.Resolver()
}

// StoreHasDEK reports whether the Store currently holds a DEK, exposing
// only the presence bit (never the key material). Tests use it to assert
// that Close wiped the key. Returns false for non-*store implementers.
func StoreHasDEK(s Store) bool {
	concrete, ok := s.(*store)
	if !ok {
		return false
	}
	return concrete.dataFacet.core.crypto.HasDEK()
}

// ReadDriverFile reads a file from the Store's underlying Driver.
// Tests use this to inspect raw on-disk manifest bytes —
// in particular to verify that Sealed leaves system fields
// in plaintext while Paranoid hides them.
func ReadDriverFile(s Store, path string) ([]byte, error) {
	concrete, ok := s.(*store)
	if !ok {
		return nil, fmt.Errorf("ReadDriverFile: not a *store")
	}
	rc, err := concrete.dataFacet.core.drv.Get(context.Background(), path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// WriteDriverFile writes raw bytes to the Store's Driver, used
// by tests that need to inject tampered manifest contents to
// verify integrity-check paths. Bypasses Put — caller is
// responsible for the resulting on-disk consistency.
func WriteDriverFile(s Store, path string, data []byte) error {
	concrete, ok := s.(*store)
	if !ok {
		return fmt.Errorf("WriteDriverFile: not a *store")
	}
	return concrete.dataFacet.core.drv.Put(context.Background(), path, bytes.NewReader(data))
}
