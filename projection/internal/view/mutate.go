package view

import (
	"os"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/projection/pathx"
)

// Close marks the View closed. Idempotent. Subsequent reads
// return os.ErrClosed.
func (v *View) Close() error {
	v.closed.Store(true)
	return nil
}

// Add registers a new manifest, mirroring backfill's per-manifest
// path. Used by FSOps after Store.Put. Concurrent with reads;
// holds the write lock.
//
// Returns os.ErrClosed if the View is closed. Otherwise nil —
// classification cannot fail for a valid manifest (the input
// itself is what the source produced).
func (v *View) Add(m domain.Manifest) error {
	if v.closed.Load() {
		return os.ErrClosed
	}
	if !v.passesFilter(m) {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	// Idempotent: an Add for an already-known ArtifactID is a no-op.
	if _, exists := v.artifacts[m.ArtifactID]; exists {
		return nil
	}
	v.indexArtifact(m, false)
	return nil
}

// Remove drops every entry of the artifact from every tree.
// Handles by-path collision re-election when the removed
// artifact was the current owner of a path.
//
// Idempotent: Remove for an unknown ArtifactID is a no-op.
func (v *View) Remove(id domain.ArtifactID) error {
	if v.closed.Load() {
		return os.ErrClosed
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	rec, ok := v.artifacts[id]
	if !ok {
		return nil
	}
	v.removeArtifactFromTrees(id, rec)
	return nil
}

// removeArtifactFromTrees does the actual fan-out delete. Caller
// holds the write lock.
func (v *View) removeArtifactFromTrees(id domain.ArtifactID, rec *artifactRecord) {
	if rec.pathByArtifact != "" {
		v.removeFile(v.byArtifact, rec.pathByArtifact)
	}
	if rec.pathByDate != "" {
		v.removeFile(v.byDate, rec.pathByDate)
	}
	if rec.pathByNamespace != "" {
		v.removeFile(v.byNamespace, rec.pathByNamespace)
	}
	if rec.pathBySession != "" {
		v.removeFile(v.bySession, rec.pathBySession)
	}
	if rec.pathByPath != "" {
		v.removeFromByPath(id, rec)
	}
	if rec.pathByOrphaned != "" {
		v.removeFile(v.byOrphaned, rec.pathByOrphaned)
	}

	delete(v.artifacts, id)
	v.Stats.TotalNodes--
	v.Stats.TotalBytes -= rec.manifest.OriginalSize
	if rec.pathByPath == "" {
		v.Stats.OrphanedCount--
	}
	// SessionCount and NamespaceCount: we do not decrement because
	// tracking "last artifact in this session" requires a counter
	// per session, which is a 3b-future complication. Stats remain
	// monotonic for those two counters across the View's lifetime —
	// callers use them for pacing, not for exact accounting.
}

// removeFromByPath drops an artifact from the by-path tree. If
// it was the current owner of a path, the freshest loser (if any)
// is promoted to owner.
func (v *View) removeFromByPath(id domain.ArtifactID, rec *artifactRecord) {
	path := rec.pathByPath
	owner, claimed := v.pathOwner[path]
	if claimed && owner == id {
		// Drop the file node and try to promote a loser.
		v.removeFile(v.byPath, path)
		delete(v.pathOwner, path)
		losers := v.pathLosers[path]
		if len(losers) > 0 {
			promoted := losers[0]
			v.pathLosers[path] = losers[1:]
			if len(v.pathLosers[path]) == 0 {
				delete(v.pathLosers, path)
			}
			promotedRec, ok := v.artifacts[promoted.id]
			if ok {
				v.pathOwner[path] = promoted.id
				v.insertFile(v.byPath, path, promotedRec.manifest)
			}
		}
	} else {
		// Removed artifact was a loser, not owner.
		v.removeLoser(path, id)
	}
}

// Move atomically replaces an old artifact with a new one — used
// by FSOps to emulate rename. The old artifact's by-path entry
// is dropped (with collision re-election), and the new manifest
// is added through the standard Add path.
//
// oldPath/newPath are passed for documentation and future use
// (FSOps wants to log the user-level rename); the actual location
// in by-path comes from the new manifest's resolver result.
func (v *View) Move(oldPath, newPath string, m domain.Manifest) error {
	if v.closed.Load() {
		return os.ErrClosed
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	// We do not require oldPath to currently exist — the FSOps
	// orchestration may have already done the Store.Delete and
	// only failed to find the manifest. Move is idempotent on the
	// "old" side: remove if present, add new.

	// Find old artifact by oldPath in by-path; if found, remove.
	if oldOwner, ok := v.pathOwner[oldPath]; ok {
		if rec, found := v.artifacts[oldOwner]; found {
			v.removeArtifactFromTrees(oldOwner, rec)
		}
	}

	// Add the new manifest, applying filter.
	if !v.passesFilter(m) {
		return nil
	}
	if _, exists := v.artifacts[m.ArtifactID]; exists {
		return nil
	}
	v.indexArtifact(m, false)
	_ = newPath
	return nil
}

// --- Internal helpers ---

// insertFile creates a file node (or updates an existing one) at
// path in tree, ensuring all parent directories exist as virtual
// nodes.
//
// FilesystemFacet carries only the schema-agnostic fields: Name,
// Path, Size, ModTime, IsDir. POSIX attributes (mode/uid/gid)
// live in vfsmeta.FileSystem inside Manifest.Ext and are
// materialised by FSOps at the transport boundary.
//
// ModTime here is seeded from m.CreatedAt as a baseline; FSOps
// overrides with vfsmeta.ModTime when non-zero.
func (v *View) insertFile(tree map[string]*viewNode, path string, m domain.Manifest) {
	v.ensureDirs(tree, pathx.Parent(path))
	name := pathx.LastSegment(path)
	tree[path] = &viewNode{
		fs: FilesystemFacet{
			Name:    name,
			Path:    path,
			IsDir:   false,
			Size:    m.OriginalSize,
			ModTime: m.CreatedAt,
		},
		artifact: artifactFacetFrom(m),
	}
	parent := pathx.Parent(path)
	if pn, ok := tree[parent]; ok {
		pn.children = insertSorted(pn.children, name)
	}
}

// removeFile deletes the node at path. Empty parent directories
// are recursively pruned to keep List tidy. The tree root ""
// always survives.
func (v *View) removeFile(tree map[string]*viewNode, path string) {
	if _, ok := tree[path]; !ok {
		return
	}
	delete(tree, path)
	parent := pathx.Parent(path)
	name := pathx.LastSegment(path)
	if pn, ok := tree[parent]; ok {
		pn.children = removeSorted(pn.children, name)
		// Prune empty virtual directory cascading upwards.
		for parent != "" && len(pn.children) == 0 && pn.artifact == nil {
			delete(tree, parent)
			grand := pathx.Parent(parent)
			gname := pathx.LastSegment(parent)
			parent = grand
			pn, ok = tree[grand]
			if !ok {
				break
			}
			pn.children = removeSorted(pn.children, gname)
		}
	}
}

// ensureDirs walks path top-down and inserts virtual directory
// nodes for every component that does not yet exist.
func (v *View) ensureDirs(tree map[string]*viewNode, path string) {
	if path == "" {
		return
	}
	segments := strings.Split(path, "/")
	cur := ""
	for i, seg := range segments {
		next := seg
		if cur != "" {
			next = cur + "/" + seg
		}
		if _, ok := tree[next]; !ok {
			tree[next] = newDirNode(seg, next, v.CreatedAt)
			parent := ""
			if i > 0 {
				parent = cur
			}
			if pn, ok := tree[parent]; ok {
				pn.children = insertSorted(pn.children, seg)
			}
		}
		cur = next
	}
}
