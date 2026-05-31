package domain

// Namespace tokens with engine-wide meaning. Production code must
// reference these constants — never the equivalent literals — so a
// future spelling change (e.g. "system.state" → "system.state/")
// is one edit, and the compiler catches every missed call site.

// NamespaceWildcard is the special token meaning "every user
// namespace" in Store.Walk and "deny" in Store.Put.
const NamespaceWildcard = "*"

// NamespaceSystemPrefix is the reserved prefix for engine-private
// namespaces. A user-supplied Namespace starting with this prefix
// is rejected at the Put boundary with errs.ErrReservedNamespace.
const NamespaceSystemPrefix = "system."

// Reserved system namespaces. The engine emits these from internal
// writers (system.config from InitStore; system.state from agents);
// user code reads them through Store.WalkSystem with a capability
// token.
const (
	// NamespaceSystemState holds engine-managed state artifacts:
	// staging blobs (system.state/staging/), the scrub cursor,
	// index snapshots, agent leases.
	NamespaceSystemState = "system.state"

	// NamespaceSystemConfig holds the active StoreConfig artifact;
	// the pointer file lives at NamespaceSystemConfig + "/current".
	NamespaceSystemConfig = "system.config"
)

// StagingPrefix is the driver path prefix for in-flight blob writes,
// under system.state. A blob is written here until its content hash
// is known, then renamed to its final hash-derived path. Living under
// system.state keeps staging blobs out of the way of other engine
// code: the bootstrap Orphan Scan treats dangling staging files as
// orphans and prunes them. Both the write path (which creates these
// files) and the Orphan Scan (which reclaims them) reference this
// constant, so the convention is defined once here.
const StagingPrefix = NamespaceSystemState + "/staging"
