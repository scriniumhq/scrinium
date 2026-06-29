package fsops

import (
	"slices"
	"strings"

	"scrinium.dev/projection/pathx"
)

func (o *Ops) isPendingDir(path string) bool {
	o.pendingDirsMu.Lock()
	defer o.pendingDirsMu.Unlock()
	_, ok := o.pendingDirs[path]
	return ok
}

// pendingDirInfo synthesises a FileInfo for a pending directory.
// Mode comes from Ops default for directories (0755).
func (o *Ops) pendingDirInfo(path string) FileInfo {
	return FileInfo{
		Name:  pathx.LastSegment(path),
		Path:  path,
		IsDir: true,
		Mode:  0o755,
		UID:   o.defaultUID,
		GID:   o.defaultGID,
	}
}

// pendingChildrenOf returns FileInfos for pending directories
// whose parent equals parent. Order is sorted by Name.
func (o *Ops) pendingChildrenOf(parent string) []FileInfo {
	o.pendingDirsMu.Lock()
	defer o.pendingDirsMu.Unlock()
	var out []FileInfo
	for p := range o.pendingDirs {
		if pathx.Parent(p) != parent {
			continue
		}
		out = append(out, FileInfo{
			Name:  pathx.LastSegment(p),
			Path:  p,
			IsDir: true,
			Mode:  0o755,
			UID:   o.defaultUID,
			GID:   o.defaultGID,
		})
	}
	// Sort for deterministic order.
	slices.SortFunc(out, func(a, b FileInfo) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// dropParentPendingDirs removes pendingDirs entries that match
// any ancestor of path. Called after a successful Add to clean
// up "pre-created" directories now backed by real children.
func (o *Ops) dropParentPendingDirs(path string) {
	o.pendingDirsMu.Lock()
	defer o.pendingDirsMu.Unlock()
	for p := range o.pendingDirs {
		// Trim entries that are an ancestor of path or equal.
		if p == "" {
			continue
		}
		if pathx.IsUnder(path, p) {
			delete(o.pendingDirs, p)
		}
	}
}

// dropPendingTree removes the pending entry for path and every pending entry
// beneath it. Called by RemoveTree so a recursive delete also clears virtual
// sub-directories that were Mkdir-created but never given a real child.
func (o *Ops) dropPendingTree(path string) {
	o.pendingDirsMu.Lock()
	defer o.pendingDirsMu.Unlock()
	for p := range o.pendingDirs {
		if p == "" {
			continue
		}
		if pathx.IsUnder(p, path) {
			delete(o.pendingDirs, p)
		}
	}
}
