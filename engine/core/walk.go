package core

// walk.go — Store.Walk and Store.WalkSystem, plus the namespace-syntax
// validators used at the API boundary. Iteration itself is delegated
// to StoreIndex.ListByNamespace; the methods here enforce the public
// contract (4. API Reference/04 §4.1, §4.2).

import (
	"context"
	"strings"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

// Walk iterates over user manifests. See docs/4. API Reference/04
// §4.1 for the contract; this implementation enforces the
// namespace-syntax rules (reject system.* prefix, length limit)
// and delegates to the StoreIndex for the actual iteration.
//
// Pack manifests are excluded by the index (they live in
// packed_blobs, never in manifests). System namespaces are
// excluded by both the index ("*" wildcard skips system.*) and by
// us at the API surface (an explicit "system.foo" gets
// errs.ErrReservedNamespace before the index sees it).
func (s *store) Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error {
	if err := s.enterRead(ctx); err != nil {
		return err
	}
	if err := validateUserNamespace(namespace); err != nil {
		return err
	}
	return s.index.ListByNamespace(ctx, namespace, cb)
}

// WalkSystem iterates over manifests inside one of the four
// reserved system namespaces. See docs/4. API Reference/04 §4.2.
// Allowed namespaces: system.transit, system.manifests,
// system.state, system.config.
//
// Capability-token enforcement is opt-in by docs and TBD by
// implementation; M1.4 honours the namespace-syntax rules but
// does not yet block calls based on token contents. Tracking:
// 4. API Reference/01 §1.3.1 (WithCapabilityToken) and the
// related authorisation work in M2.
func (s *store) WalkSystem(ctx context.Context, namespace string, cb func(domain.Manifest) error) error {
	if err := s.enterRead(ctx); err != nil {
		return err
	}
	if !isSystemNamespace(namespace) {
		return errs.ErrReservedNamespace
	}
	return s.index.ListByNamespace(ctx, namespace, cb)
}

// validateUserNamespace enforces the contract of Walk's namespace
// argument. See docs §4.1.
func validateUserNamespace(ns string) error {
	if len(ns) > domain.MaxNamespaceLen {
		return errs.ErrNamespaceTooLong
	}
	// "*" and "" are valid (wildcard / default namespace). Any
	// "system." prefix is reserved.
	if strings.HasPrefix(ns, domain.NamespaceSystemPrefix) {
		return errs.ErrReservedNamespace
	}
	return nil
}

// isSystemNamespace reports whether the given string is one of the
// four reserved system namespace names. Wildcard ("*") and empty
// ("") are deliberately excluded — see docs §4.2 for the
// rationale.
func isSystemNamespace(ns string) bool {
	switch ns {
	case domain.NamespaceSystemTransit,
		domain.NamespaceSystemManifests,
		domain.NamespaceSystemState,
		domain.NamespaceSystemConfig:
		return true
	}
	return false
}
