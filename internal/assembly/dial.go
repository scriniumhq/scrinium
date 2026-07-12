package assembly

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	decl "scrinium.dev/config/declarative"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/internal/uri"
)

// dialDriver resolves the store's driver: a custom index factory if one
// is registered for the scheme, otherwise the engine's built-in
// DialDriver (file://, s3:// when present, bare paths). The built-in
// schemes are registered by the consumer's blank import (ADR-63).
func dialDriver(ctx context.Context, spec *decl.StoreSpec) (driver.Driver, error) {
	scheme := uri.SchemeOf(spec.Driver)
	if f, ok := globalRegistry.driver(scheme); ok {
		creds, err := resolveCredentials(ctx, spec.Credentials)
		if err != nil {
			return nil, err
		}
		return f(ctx, spec.Driver, creds)
	}
	return driver.DialDriver(ctx, spec.Driver)
}

// dialIndex resolves the index along the default ladder (ADR-63): an
// explicit spec.Index wins; else Config.Defaults.Index; else a built-in
// sqlite in the store's index/ dir. The resolved URI is dialled through an
// custom index factory if one is registered for its scheme, otherwise the
// engine's built-in DialIndex.
func dialIndex(ctx context.Context, spec *decl.StoreSpec, defaults *decl.Defaults) (index.StoreIndex, error) {
	indexUri := spec.Index
	if indexUri == "" && defaults != nil {
		indexUri = defaults.Index
	}
	if indexUri == "" {
		p, err := uri.ResolveLocalURI(spec.Driver)
		if err != nil {
			return nil, fmt.Errorf("index URI is empty and cannot default for store %q (set index explicitly)", spec.Driver)
		}
		indexUri = "sqlite:///" + filepath.Join(p, "index", "index.db")
	}
	if f, ok := globalRegistry.indexFor(uri.SchemeOf(indexUri)); ok {
		creds, err := resolveCredentials(ctx, spec.Credentials)
		if err != nil {
			return nil, err
		}
		return f(ctx, indexUri, creds)
	}
	return index.DialIndex(ctx, indexUri)
}

// dialBackends constructs the driver and index and, for a non-open mode on
// a local store, ensures the store directory exists. The index is the first
// rollback-registered resource.
func (bs *buildState) dialBackends() error {
	// 1. Driver.
	drv, err := dialDriver(bs.ctx, bs.spec)
	if err != nil {
		return fmt.Errorf("scrinium: driver: %w", err)
	}
	bs.drv = drv

	// 2. For an Init/OpenOrInit on a local store, ensure the directory.
	if bs.mode != modeOpen {
		if p, perr := uri.ResolveLocalURI(bs.spec.Driver); perr == nil {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return fmt.Errorf("scrinium: mkdir store: %w", err)
			}
		}
	}

	// 3. Index (default ladder: explicit spec.Index, then Config.Defaults,
	//    then a built-in sqlite next to a local store).
	idx, err := dialIndex(bs.ctx, bs.spec, bs.c.Defaults)
	if err != nil {
		return fmt.Errorf("scrinium: index: %w", err)
	}
	bs.idx = idx
	bs.cleanups = append(bs.cleanups, func() {
		if err := idx.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium: index close on rollback: %v\n", err)
		}
	})
	return nil
}
