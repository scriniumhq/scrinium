package fsops

import "scrinium.dev/domain"

// Option configures New.
type Option func(*fsOpsOptions)

type fsOpsOptions struct {
	store        StoreClient
	scratchDir   string
	scratchQuota int64
	defaultMode  uint32
	defaultUID   uint32
	defaultGID   uint32
	editing      EditingPolicy
	mountSession domain.SessionID
	readOnly     bool
}

// EditingPolicy is the per-operation switchboard for the editing
// surface (rename, setattr, truncate, append). Each bit is
// independent; helpers EditingOff / EditingOn are sugar.
//
// AllowRename governs file rename. Directory rename — both the empty
// pending-dir case and the recursive non-empty case — is a separate
// capability under AllowDirRename, because a recursive rename rewrites
// every descendant's manifest (one Put+Delete per child).
type EditingPolicy struct {
	AllowRename    bool
	AllowSetattr   bool
	AllowTruncate  bool
	AllowAppend    bool
	AllowDirRename bool
}

// EditingOff is the conservative default: no editing of existing
// artifacts. Create and Unlink still work.
func EditingOff() EditingPolicy { return EditingPolicy{} }

// EditingOn enables every editing capability. Use only when
// callers understand the CAS implications (every mutation
// produces a new artifact).
func EditingOn() EditingPolicy {
	return EditingPolicy{
		AllowRename:    true,
		AllowSetattr:   true,
		AllowTruncate:  true,
		AllowAppend:    true,
		AllowDirRename: true,
	}
}

func WithStore(s StoreClient) Option {
	return func(o *fsOpsOptions) { o.store = s }
}

// WithScratchDir sets the directory for scratch files. Must
// exist and be writable. Scratch is created lazily on the first
// Create/Open(write).
func WithScratchDir(path string) Option {
	return func(o *fsOpsOptions) { o.scratchDir = path }
}

// WithScratchQuota caps the total bytes held by active scratch
// files. 0 means unlimited.
func WithScratchQuota(bytes int64) Option {
	return func(o *fsOpsOptions) { o.scratchQuota = bytes }
}

// WithDefaultMode is the POSIX mode applied to artifacts whose
// vfsmeta.Mode is zero. Default 0644.
func WithDefaultMode(mode uint32) Option {
	return func(o *fsOpsOptions) { o.defaultMode = mode }
}

// WithDefaultUID applies to vfsmeta.UID == 0.
func WithDefaultUID(uid uint32) Option {
	return func(o *fsOpsOptions) { o.defaultUID = uid }
}

// WithDefaultGID applies to vfsmeta.GID == 0.
func WithDefaultGID(gid uint32) Option {
	return func(o *fsOpsOptions) { o.defaultGID = gid }
}

// WithEditingPolicy gates editing operations. Default:
// EditingOff.
func WithEditingPolicy(p EditingPolicy) Option {
	return func(o *fsOpsOptions) { o.editing = p }
}

// WithMountSession sets the SessionID stamped onto every Put
// performed through Ops in this mount. Empty means "no
// session" — artifacts will not appear in by-session.
func WithMountSession(sid domain.SessionID) Option {
	return func(o *fsOpsOptions) { o.mountSession = sid }
}

// WithReadOnly forces every mutation to return ErrEditingDisabled
// regardless of EditingPolicy or the existence of a StoreClient.
func WithReadOnly() Option {
	return func(o *fsOpsOptions) { o.readOnly = true }
}
