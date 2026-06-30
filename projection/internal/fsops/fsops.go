package fsops

import (
	"context"
	"fmt"
	"sync"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/internal/keyedlock"
	vw "scrinium.dev/projection/internal/view"
)

// Ops is the filesystem-shaped operations layer over a View.
// It serves transports (FUSE, WebDAV) by translating
// path-keyed POSIX-like calls into View lookups (read side) and,
// in stage 4b, into Store mutations (write side).
//
// The two reasons Ops exists rather than the transport calling
// View directly:
//
//  1. Editing policy, scratch buffering, path-level locks live in
//     one place — FUSE and WebDAV inherit them by construction.
//  2. The transport works in terms of the configured root tree
//     (RootView). Ops hides that routing from the caller.
//
// Stage 4a: read-side (Stat, Listdir, Open). Mutations land in 4b.
type Ops struct {
	view *vw.View

	store StoreClient

	scratchDir   string
	scratchQuota int64

	defaultMode uint32
	defaultUID  uint32
	defaultGID  uint32

	editing      EditingPolicy
	mountSession domain.SessionID
	readOnly     bool

	// Path locks: per-path RWMutex serialising mutations on a
	// single path while permitting concurrent readers.
	pathLocks *keyedlock.Map

	// quota tracks the total bytes held across all live scratch
	// files. Reserve/Release pair around each WriteAt.
	quota *quotaTracker

	// pendingDirs holds virtual directories created via Mkdir
	// that have no children yet, so Stat/Listdir can see them
	// before any file lands inside. Cleared on Rmdir or when a
	// real child appears (see Listdir/Stat — pendingDirs are
	// not removed there; the directory simply moves into the
	// View when ensureDirs runs at Add time).
	pendingDirsMu sync.Mutex
	pendingDirs   map[string]struct{}
}

const (
	OpenReadOnly  OpenMode = 0
	OpenWriteOnly OpenMode = 1
	OpenReadWrite OpenMode = 2
	OpenAppend    OpenMode = 4
)

// New wraps a View with filesystem-shaped operations.
//
// The View must already exist (the caller is responsible for the
// View's lifecycle); New does not call view.New itself
// because the View may be shared with other transports.
//
// Defaults:
//
//   - DefaultMode 0644, DefaultUID/GID 0
//   - EditingOff (no rename, setattr, truncate, append)
//   - ScratchQuota 0 (unlimited; the OS still imposes its own)
//
// Returns an error only if v is nil. Configuration sanity is
// otherwise the caller's responsibility (e.g. an invalid
// scratch dir surfaces only at the first Create call).
func New(v *vw.View, opts ...Option) (*Ops, error) {
	if v == nil {
		return nil, fmt.Errorf("projection.New: view is nil")
	}
	o := fsOpsOptions{
		defaultMode: 0o644,
	}
	for _, opt := range opts {
		opt(&o)
	}
	// Reclaim scratch files orphaned by a previous run (e.g. a daemon
	// killed before Close could clean up). Only an explicitly configured
	// dir is swept — the default OS temp dir is shared and not ours to
	// touch. Best-effort: errors are ignored and startup never blocks.
	reapStaleScratch(o.scratchDir)
	return &Ops{
		view:         v,
		store:        o.store,
		scratchDir:   o.scratchDir,
		scratchQuota: o.scratchQuota,
		defaultMode:  o.defaultMode,
		defaultUID:   o.defaultUID,
		defaultGID:   o.defaultGID,
		editing:      o.editing,
		mountSession: o.mountSession,
		readOnly:     o.readOnly,
		pathLocks:    keyedlock.New(),
		quota:        &quotaTracker{quota: o.scratchQuota},
		pendingDirs:  make(map[string]struct{}),
	}, nil
}

// --- Read-side helpers (router into View per RootView) ---

// lookupInRoot routes Get to the tree configured as the root.
func (o *Ops) lookupInRoot(path string) (vw.Node, error) {
	return o.view.GetIn(o.view.RootView(), path)
}

func (o *Ops) listInRoot(path string) vw.Seq {
	return o.view.ListIn(o.view.RootView(), path)
}

func (o *Ops) openInRoot(ctx context.Context, path string) (domain.ReadHandle, error) {
	return o.view.OpenIn(ctx, o.view.RootView(), path)
}

// fileInfoFromNode converts a Node into a FileInfo, applying
// Ops defaults to fields the artifact left zero.
//
// For file nodes, Ops reads vfsmeta from Manifest.Ext directly
// — the View itself is schema-agnostic and does not surface
// vfsmeta.Mode/UID/GID/ModTime through FilesystemFacet. Ops is
// the layer that knows about the filesystem schema and applies
// defaults at the boundary where they have to be visible to
// FUSE/WebDAV.
//
// Decode errors are silently swallowed: the same hot-path policy
// as vfsmeta.Resolver inside View. A single bad ext payload must
// not poison Stat/Listdir for the whole tree.
func (o *Ops) fileInfoFromNode(n vw.Node) FileInfo {
	fi := FileInfo{
		Name:    n.FS.Name,
		Path:    n.FS.Path,
		Size:    n.FS.Size,
		ModTime: n.FS.ModTime,
		IsDir:   n.FS.IsDir,
	}

	// For files, prefer vfsmeta-encoded attributes when present.
	// For virtual directories, n.Artifact is nil — defaults are
	// the only available source.
	if n.Artifact != nil {
		fi.ArtifactID = n.Artifact.ArtifactID
		if fs, ok, err := vfsmeta.Decode(n.Artifact.Ext); err == nil && ok {
			fi.Mode = fs.Mode
			fi.UID = fs.UID
			fi.GID = fs.GID
			fi.MIME = fs.MIME
			if !fs.ModTime.IsZero() {
				fi.ModTime = fs.ModTime
			}
		}
	}

	if fi.Mode == 0 {
		if fi.IsDir {
			fi.Mode = 0o755
		} else {
			fi.Mode = o.defaultMode
		}
	}
	if fi.UID == 0 {
		fi.UID = o.defaultUID
	}
	if fi.GID == 0 {
		fi.GID = o.defaultGID
	}
	return fi
}

// Compile-time guards.
var (
	_ Handle = (*readOnlyFile)(nil)
)
