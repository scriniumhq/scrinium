package assembly

import (
	"context"
	"fmt"
	"path/filepath"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/internal/uri"
)

// dialDriver resolves the store's driver: a custom index factory if one
// is registered for the scheme, otherwise the engine's built-in
// DialDriver (file://, s3:// when present, bare paths). The built-in
// schemes are registered by the consumer's blank import (ADR-63).
func dialDriver(ctx context.Context, spec *StoreSpec) (driver.Driver, error) {
	scheme := uri.SchemeOf(spec.Driver)
	if f, ok := reg.driver(scheme); ok {
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
func dialIndex(ctx context.Context, spec *StoreSpec, defaults *Defaults) (index.StoreIndex, error) {
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
	if f, ok := reg.indexFor(uri.SchemeOf(indexUri)); ok {
		creds, err := resolveCredentials(ctx, spec.Credentials)
		if err != nil {
			return nil, err
		}
		return f(ctx, indexUri, creds)
	}
	return index.DialIndex(ctx, indexUri)
}
