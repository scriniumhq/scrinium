package domain

// Namespace tokens with engine-wide meaning. Production code must
// reference these constants — never the equivalent literals — so a
// future spelling change (e.g. "system.transit" → "system.transit/")
// is one edit, and the compiler catches every missed call site.

// NamespaceWildcard is the special token meaning "every user
// namespace" in Store.Walk and "deny" in Store.Put.
const NamespaceWildcard = "*"

// NamespaceSystemPrefix is the reserved prefix for engine-private
// namespaces. A user-supplied Namespace starting with this prefix
// is rejected at the Put boundary with errs.ErrReservedNamespace.
const NamespaceSystemPrefix = "system."

// Reserved system namespaces. The engine emits these from internal
// writers (system.config from InitStore; system.state from agents;
// system.transit and system.manifests from Curator+bundler in M3+);
// user code reads them through Store.WalkSystem with a capability
// token.
const (
	// NamespaceSystemTransit holds files staged in HostStorage's
	// transit area before Drain delivers them to a Target.
	NamespaceSystemTransit = "system.transit"

	// NamespaceSystemManifests holds bundler-produced .pack
	// manifests (engine-internal; invisible through Walk).
	NamespaceSystemManifests = "system.manifests"

	// NamespaceSystemState holds engine-managed state artifacts:
	// staging blobs (system.state/staging/), the scrub cursor,
	// index snapshots, agent leases.
	NamespaceSystemState = "system.state"

	// NamespaceSystemConfig holds the active StoreConfig artifact;
	// the pointer file lives at NamespaceSystemConfig + "/current".
	NamespaceSystemConfig = "system.config"
)
