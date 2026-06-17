package view

import (
	"context"
	"fmt"
	"os"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
)

// --- Public accessors ---

// RootView returns the configured root tree. It is informational
// metadata: the View itself does not hide other trees, but
// transports (FUSE, FSOps) read this to decide which tree to
// surface in the mount root and which to relegate to the service
// directory.
//
// Stable for the lifetime of the View — the option is set at
// New time and never mutated.
func (v *View) RootView() RootView { return v.opts.rootView }

// StatsSnapshot returns a copy of the View's current counters. It is
// the method form of the Stats field, so the read-only projection
// surface can expose stats through an interface without leaking the
// field (and thus the View type) to external callers.
func (v *View) StatsSnapshot() Stats { return v.Stats }

// SourceName returns the source kind as a string (e.g. "store",
// "multistore"). Method form of the Source field, for the same
// interface-exposure reason as StatsSnapshot.
func (v *View) SourceName() string { return string(v.Source) }

// --- Read methods (one set per tree) ---

// LookupLocations returns the per-tree paths of an artifact.
// Used by the web artifact details page to surface "show me
// where this lives" links into each tree's listing.
//
// (zero, false) if the artifact isn't tracked.
func (v *View) LookupLocations(id domain.ArtifactID) (Locations, bool) {
	if v.closed.Load() {
		return Locations{}, false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	rec, ok := v.artifacts[id]
	if !ok {
		return Locations{}, false
	}
	return Locations{
		ByArtifact:  rec.paths[RootByArtifact],
		BySession:   rec.paths[RootBySession],
		ByNamespace: rec.paths[RootByNamespace],
		ByDate:      rec.paths[RootByDate],
		ByPath:      rec.paths[RootByPath],
		ByOrphaned:  rec.paths[RootByOrphaned],
	}, true
}

// Search scans the View for artifacts matching the query.
// Substring matching, case-insensitive, against:
//
//   - the artifact's by-path placement (covers vfsmeta names);
//   - the namespace field;
//   - an exact ArtifactID match (so users can paste an id and
//     jump straight to it).
//
// limit caps the result count; passing 0 disables the cap (use
// only for diagnostic flows). Order matches the scan order over
// the artifacts map — random-but-stable within a single View
// state. Callers sort if they need a specific order.
//
// Implementation is the same linear scan as RelatedByBlobRef:
// O(N) under RLock, fast for stores up to ~100K artifacts.
// Beyond that, we'd want an actual search index — see backlog.
func (v *View) Search(query string, limit int) []SearchResult {
	if v.closed.Load() {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	var out []SearchResult
	for id, rec := range v.artifacts {
		// Exact id match — strongest signal, surface first.
		if string(id) == query {
			out = append(out, makeSearchResult(id, rec, "id"))
			if limit > 0 && len(out) >= limit {
				return out
			}
			continue
		}

		path := strings.ToLower(rec.paths[RootByPath])
		ns := strings.ToLower(rec.manifest.Namespace)

		switch {
		case path != "" && strings.Contains(path, q):
			out = append(out, makeSearchResult(id, rec, "path"))
		case ns != "" && strings.Contains(ns, q):
			out = append(out, makeSearchResult(id, rec, "namespace"))
		default:
			continue
		}
		if limit > 0 && len(out) >= limit {
			return out
		}
	}
	return out
}

// makeSearchResult populates a SearchResult from an artifact
// record. MIME is best-effort from vfsmeta; absence falls back
// to empty (the UI is responsible for any custom index-based
// inference it cares about).
func makeSearchResult(id domain.ArtifactID, rec *artifactRecord, reason string) SearchResult {
	r := SearchResult{
		ArtifactID:  id,
		Path:        rec.paths[RootByPath],
		Namespace:   rec.manifest.Namespace,
		SessionID:   rec.manifest.SessionID,
		CreatedAt:   rec.manifest.CreatedAt,
		MatchReason: reason,
	}
	if fs, ok, err := vfsmeta.Decode(rec.manifest.Ext); err == nil && ok {
		r.MIME = fs.MIME
	}
	return r
}

// RelatedByBlobRef returns every artifact that shares the given
// BlobRef, excluding the artifact identified by `exclude`.
// Useful for the "this blob is also used here" web view —
// one of the few introspections specific to a CAS store.
//
// Implementation is a linear scan of the artifacts map. That
// scales to roughly 100K artifacts inside a single web request
// without blocking; bigger stores will want an index by
// blob_ref. We accept the linearity now because the alternative
// (push the query into store.Store/index) costs more wiring than
// the value justifies at this scale.
//
// Concurrency: holds RLock for the scan duration. A
// long-running scan would block writers; the 100K-artifact
// budget keeps it under ~10ms in practice.
func (v *View) RelatedByBlobRef(blobRef domain.BlobRef, exclude domain.ArtifactID) []RelatedArtifact {
	if v.closed.Load() {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()

	var out []RelatedArtifact
	for id, rec := range v.artifacts {
		if id == exclude {
			continue
		}
		if len(rec.manifest.BlobRefs) == 0 || rec.manifest.BlobRefs[0] != blobRef {
			continue
		}
		out = append(out, RelatedArtifact{
			ArtifactID: id,
			Path:       rec.paths[RootByPath],
			Namespace:  rec.manifest.Namespace,
			SessionID:  rec.manifest.SessionID,
			CreatedAt:  rec.manifest.CreatedAt,
		})
	}
	return out
}

// --- Root-view dispatchers ---
//
// GetIn, ListIn, OpenIn and WalkIn select a tree by RootView
// enum and operate within it. This is the only read access into
// the per-tree maps: callers that hold a RootView (the vfs facade
// routing a path, service-tree listing) go through these instead
// of reaching for a per-tree method.
//
// An unknown RootView returns ErrPathNotFound for Get/Open and
// a single-shot error sequence for List, matching the behaviour
// service callers expect when a configuration drifts.

// GetIn returns the node at path within the rv tree.
func (v *View) GetIn(rv RootView, path string) (Node, error) {
	tree := v.treeFor(rv)
	if tree == nil {
		return Node{}, errs.ErrPathNotFound
	}
	return v.getInTree(tree, path)
}

// ListIn lists the immediate children at path within the rv tree.
func (v *View) ListIn(rv RootView, path string) Seq {
	tree := v.treeFor(rv)
	if tree == nil {
		return func(yield func(Node, error) bool) {
			yield(Node{}, errs.ErrPathNotFound)
		}
	}
	return v.listInTree(tree, path)
}

// OpenIn opens an artifact at path within the rv tree.
func (v *View) OpenIn(ctx context.Context, rv RootView, path string, opts ...domain.GetOption) (domain.ReadHandle, error) {
	tree := v.treeFor(rv)
	if tree == nil {
		return nil, errs.ErrPathNotFound
	}
	return v.openInTree(ctx, tree, path, opts...)
}

// Open fetches an artifact's read handle by id, bypassing the tree
// lookup. The handle also carries the manifest, so callers that only
// need metadata can read rh.Manifest() and close immediately. This is
// the read path surfaces use when they already hold an id (web view,
// download) rather than a tree path.
func (v *View) Open(ctx context.Context, id domain.ArtifactID) (domain.ReadHandle, error) {
	return v.src.Get(ctx, id)
}

// WalkIn iterates every node at or under prefix within the rv
// tree. An unknown RootView yields a single-shot error sequence,
// matching ListIn.
func (v *View) WalkIn(rv RootView, prefix string) Seq {
	tree := v.treeFor(rv)
	if tree == nil {
		return func(yield func(Node, error) bool) {
			yield(Node{}, errs.ErrPathNotFound)
		}
	}
	return v.walkInTree(tree, prefix)
}

// treeFor returns the internal tree for the given RootView, or
// nil for a root no active view populates. Private — outside callers
// go through GetIn/ListIn/OpenIn, which absorb the nil check.
func (v *View) treeFor(rv RootView) map[string]*viewNode {
	return v.trees[rv]
}

// --- Per-tree implementations ---

func (v *View) getInTree(tree map[string]*viewNode, path string) (Node, error) {
	if v.closed.Load() {
		return Node{}, os.ErrClosed
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	n, ok := tree[path]
	if !ok {
		return Node{}, fmt.Errorf("%w: %q", errs.ErrPathNotFound, path)
	}
	return v.exportNode(n), nil
}

func (v *View) listInTree(tree map[string]*viewNode, path string) Seq {
	return func(yield func(Node, error) bool) {
		if v.closed.Load() {
			yield(Node{}, os.ErrClosed)
			return
		}
		v.mu.RLock()
		defer v.mu.RUnlock()

		n, ok := tree[path]
		if !ok {
			yield(Node{}, fmt.Errorf("%w: %q", errs.ErrPathNotFound, path))
			return
		}
		if !n.fs.IsDir {
			yield(Node{}, fmt.Errorf("%w: %q", errs.ErrNotADirectory, path))
			return
		}
		names := append([]string(nil), n.children...)
		for _, name := range names {
			childPath := name
			if path != "" {
				childPath = path + "/" + name
			}
			child, ok := tree[childPath]
			if !ok {
				continue
			}
			if !yield(v.exportNode(child), nil) {
				return
			}
		}
	}
}

func (v *View) walkInTree(tree map[string]*viewNode, prefix string) Seq {
	return func(yield func(Node, error) bool) {
		if v.closed.Load() {
			yield(Node{}, os.ErrClosed)
			return
		}
		v.mu.RLock()
		defer v.mu.RUnlock()

		root, ok := tree[prefix]
		if !ok {
			yield(Node{}, fmt.Errorf("%w: %q", errs.ErrPathNotFound, prefix))
			return
		}
		var stack []*viewNode
		stack = append(stack, root)
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !yield(v.exportNode(n), nil) {
				return
			}
			if n.fs.IsDir {
				for i := len(n.children) - 1; i >= 0; i-- {
					name := n.children[i]
					childPath := name
					if n.fs.Path != "" {
						childPath = n.fs.Path + "/" + name
					}
					if c, ok := tree[childPath]; ok {
						stack = append(stack, c)
					}
				}
			}
		}
	}
}

func (v *View) openInTree(
	ctx context.Context,
	tree map[string]*viewNode,
	path string,
	opts ...domain.GetOption,
) (domain.ReadHandle, error) {
	if v.closed.Load() {
		return nil, os.ErrClosed
	}
	v.mu.RLock()
	n, ok := tree[path]
	v.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", errs.ErrPathNotFound, path)
	}
	if n.fs.IsDir {
		return nil, fmt.Errorf("%w: %q", errs.ErrIsADirectory, path)
	}
	rh, err := v.src.Get(ctx, n.artifact.ArtifactID, opts...)
	if err != nil {
		return nil, mapSourceError(err)
	}
	return rh, nil
}
