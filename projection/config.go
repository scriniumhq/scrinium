package projection

import "scrinium.dev/domain"

// Config is the build-time configuration for Build. It is the
// projection-layer view of the host's settings: the composition root
// (e.g. internal/assembly) maps its own config representation onto
// this struct. Unlike a serialization config it carries no tags and
// no store/policy concerns — only what the View and FSOps need.
//
// Zero values mean "engine default": an empty RootView selects the
// by-path tree, a zero ScratchQuota is unlimited, zero DefaultUID/GID
// fall back to the running process.
type Config struct {
	// RootView selects the tree presented at the mount root
	// (by-path, by-date, by-session, by-namespace, by-artifact,
	// orphaned). Empty = by-path.
	RootView string

	// PathResolver extracts the by-path key from a manifest. The
	// composition root supplies it from the view-providing extension
	// (fspath) via its ViewProvider capability (ADR-98); the projection
	// no longer hardcodes a metadata schema. nil ⇒ the by-path tree is
	// fallback-only (empty under FallbackOrphaned).
	PathResolver func(m domain.Manifest) (path string, ok bool)

	// ByPathFallback controls what the by-path tree does with
	// manifests that carry no path: "orphaned" or "synthetic".
	ByPathFallback string

	// Editing controls in-place edits: "off" (strict CAS), "on", or
	// "custom" (consult the Allow* flags). Empty = off.
	Editing       string
	AllowRename   *bool
	AllowSetattr  *bool
	AllowTruncate *bool
	AllowAppend   *bool

	// Namespace constrains writes/visibility to a single namespace.
	// Empty = global.
	Namespace string

	// ScratchDir is the staging area for in-flight FSOps writes,
	// already resolved by the caller (empty = none; the engine
	// tolerates this for read-mostly use). Ignored when ReadOnly.
	ScratchDir string

	// ScratchQuota caps the scratch area in bytes. Zero = unlimited.
	ScratchQuota int64

	// ReadOnly disables writes through FSOps.
	ReadOnly bool

	// Default POSIX bits for artifacts written without explicit ones.
	// Zero UID/GID fall back to the running process.
	DefaultMode uint32
	DefaultUID  uint32
	DefaultGID  uint32

	// MountSession tags writes from this projection instance.
	MountSession domain.SessionID
}
