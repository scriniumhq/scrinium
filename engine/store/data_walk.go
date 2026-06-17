package store

import (
	"context"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// Walk iterates over user manifests. It enforces the namespace-syntax
// rules (length limit) and delegates iteration to the StoreIndex.
//
// Headless pack containers are excluded by the index (they carry no
// handle, so the artifact_id filter skips them).
func (d dataFacet) Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error {
	if err := d.enterRead(ctx); err != nil {
		return err
	}
	if err := validateUserNamespace(namespace); err != nil {
		return err
	}
	return d.index.ListByNamespace(ctx, namespace, cb)
}

// WalkByExt iterates over user manifests whose projected ext field
// extName.field equals value (proj_ext, §9.6), delegating to the StoreIndex.
// It is extension-agnostic: the core attaches no meaning to extName/field —
// a namespace extension lists its artifacts via WalkByExt("namespace",
// "nsid", <nsid>). No namespace-syntax validation applies (extName/field/
// value are opaque projection coordinates, not a namespace label).
func (d dataFacet) WalkByExt(ctx context.Context, extName, field, value string, cb func(domain.Manifest) error) error {
	if err := d.enterRead(ctx); err != nil {
		return err
	}
	return d.index.ListByExtField(ctx, extName, field, value, cb)
}

// validateUserNamespace enforces the syntax of Walk's namespace argument.
// "*" and "" are valid (wildcard / default namespace).
func validateUserNamespace(ns string) error {
	if len(ns) > domain.MaxNamespaceLen {
		return errs.ErrNamespaceTooLong
	}
	return nil
}
