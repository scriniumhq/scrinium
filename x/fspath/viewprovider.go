package fspath

import (
	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/engine/customindex"
)

// ProvidedViews implements customindex.ViewProvider (ADR-98): the fspath
// extension backs the by-path view. The resolver is vfsmeta.Resolver
// (manifest → path from the vfsmeta payload); the registered index itself
// doubles as the bulk Metadata source the backfill consults. fspath thus
// occupies the view capability alongside the index axis (CustomIndex with
// the Indexer + Accessor capabilities) — which is what "its view together
// with the index" means.
//
// The Root string must match the projection's RootView name for by-path;
// it is kept as a literal here so the extension takes no dependency on the
// projection library (the dependency only ever runs the other way, and
// projection must not import extensions — ADR-89).
func (e *CustomIndex) ProvidedViews() []customindex.ProvidedView {
	return []customindex.ProvidedView{{
		Root:     "by-path",
		Path:     vfsmeta.Resolver,
		Collide:  true,
		Orphans:  true,
		Metadata: e,
	}}
}

// Compile-time conformance: fspathindex backs a projection view.
var _ customindex.ViewProvider = (*CustomIndex)(nil)
