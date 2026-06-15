package store

import (
	"context"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// Walk iterates over user manifests. It enforces the namespace-syntax
// rules (reject system.* prefix, length limit) and delegates iteration
// to the StoreIndex.
//
// Headless pack containers are excluded by the index (they carry no
// handle, so the artifact_id filter skips them). System namespaces are
// excluded both by the index
// (the "*" wildcard skips system.*) and here at the API surface (an
// explicit "system.foo" gets errs.ErrReservedNamespace first).
func (d dataFacet) Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error {
	if err := d.enterRead(ctx); err != nil {
		return err
	}
	if err := validateUserNamespace(namespace); err != nil {
		return err
	}
	return d.index.ListByNamespace(ctx, namespace, cb)
}

// validateUserNamespace enforces the syntax of Walk's namespace argument.
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
