package extension

import (
	"context"
	"fmt"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
)

// scopeRoot is the reserved first token of every scoped system-artifact
// name. An extension's private space is "extension.<name>."; the engine's
// own artifacts (config, checkpoints, leases) never carry it, so the two
// never collide.
const scopeRoot = "extension."

// ScopedSystemStore is a SystemStore handle confined to one extension's
// private name space. Every name an extension passes is transparently
// prefixed with "extension.<name>." before it reaches the backing
// SystemStore, and that prefix is stripped from the names Walk reports.
// An extension therefore addresses its own artifacts by short, local
// names and cannot read, write, or enumerate another extension's (or the
// engine's) artifacts: the prefix is prepended here, never supplied by
// the caller, so there is no way to escape the scope.
//
// It satisfies store.SystemStore, so an extension can pass it anywhere a
// SystemStore is expected — e.g. as the backing store of a versioned
// registry built on the namedstore Keep contract (ADR-85/101).
type ScopedSystemStore struct {
	name   string
	prefix string
	sys    store.SystemStore
}

var _ store.SystemStore = (*ScopedSystemStore)(nil)

// NewScopedSystemStore confines sys to the extension named name. name
// must be a single clean token — non-empty and free of '.', '/', and
// whitespace — so one extension's scope can neither overlap nor escape
// another's.
func NewScopedSystemStore(name string, sys store.SystemStore) (*ScopedSystemStore, error) {
	if err := validateScopeName(name); err != nil {
		return nil, err
	}
	if sys == nil {
		return nil, fmt.Errorf("extension: scoped system store %q: nil backing store", name)
	}
	return &ScopedSystemStore{name: name, prefix: scopeRoot + name + ".", sys: sys}, nil
}

// Name reports the extension scope this handle is bound to.
func (s *ScopedSystemStore) Name() string { return s.name }

// Put writes a with its Name scoped to this extension.
func (s *ScopedSystemStore) Put(ctx context.Context, a store.SystemArtifact) error {
	scoped, err := s.scopeName(a.Name)
	if err != nil {
		return err
	}
	a.Name = scoped
	return s.sys.Put(ctx, a)
}

// Get opens the named artifact within this extension's scope.
func (s *ScopedSystemStore) Get(ctx context.Context, name string) (domain.ReadHandle, error) {
	scoped, err := s.scopeName(name)
	if err != nil {
		return nil, err
	}
	return s.sys.Get(ctx, scoped)
}

// Delete removes the named artifact within this extension's scope.
func (s *ScopedSystemStore) Delete(ctx context.Context, name string) error {
	scoped, err := s.scopeName(name)
	if err != nil {
		return err
	}
	return s.sys.Delete(ctx, scoped)
}

// Walk enumerates the extension's artifacts whose local name starts with
// prefix (prefix "" walks the whole scope). Names handed to cb are local:
// the "extension.<name>." prefix is stripped, so the callback sees the
// same short names the extension wrote.
func (s *ScopedSystemStore) Walk(ctx context.Context, prefix string, cb func(name string, m domain.Manifest) error) error {
	return s.sys.Walk(ctx, s.prefix+prefix, func(name string, m domain.Manifest) error {
		return cb(strings.TrimPrefix(name, s.prefix), m)
	})
}

// scopeName prefixes a non-empty local name with the extension scope.
func (s *ScopedSystemStore) scopeName(local string) (string, error) {
	if local == "" {
		return "", fmt.Errorf("extension %q: empty artifact name", s.name)
	}
	return s.prefix + local, nil
}

// validateScopeName enforces that a scope token is a single clean segment,
// so it cannot overlap or escape another extension's space.
func validateScopeName(name string) error {
	if name == "" {
		return fmt.Errorf("extension: empty scope name")
	}
	if strings.ContainsAny(name, "./ \t\n") {
		return fmt.Errorf("extension: scope name %q must be a single token (no '.', '/', or whitespace)", name)
	}
	return nil
}
