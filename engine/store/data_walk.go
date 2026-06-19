package store

import (
	"context"

	"scrinium.dev/domain"
)

// Walk iterates over every user manifest, namespace-agnostic — the core
// attaches no namespace meaning. Namespace-filtered traversal is an
// extension concern (WalkByExt over the extension's projection).
//
// Headless pack containers are excluded by the index (they carry no
// handle, so the artifact_id filter skips them).
func (s *store) Walk(ctx context.Context, cb func(domain.Manifest) error) error {
	if err := s.enterRead(ctx); err != nil {
		return err
	}
	return s.index.IterateManifests(ctx, cb)
}

// WalkByExt iterates over user manifests whose projected ext field
// extName.field equals value (proj_ext, §9.6), delegating to the StoreIndex.
// It is extension-agnostic: the core attaches no meaning to extName/field —
// a namespace extension lists its artifacts via WalkByExt("namespace",
// "nsid", <nsid>). No namespace-syntax validation applies (extName/field/
// value are opaque projection coordinates, not a namespace label).
func (s *store) WalkByExt(ctx context.Context, extName, field, value string, cb func(domain.Manifest) error) error {
	if err := s.enterRead(ctx); err != nil {
		return err
	}
	return s.index.ListByExtField(ctx, extName, field, value, cb)
}
