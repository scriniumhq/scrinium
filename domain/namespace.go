package domain

// Namespace tokens with engine-wide meaning. Production code must
// reference these constants — never the equivalent literals — so a
// future spelling change (e.g. ".staging" → ".stg") is one edit, and
// the compiler catches every missed call site.

// NamespaceWildcard is the special token meaning "every user
// namespace" in Store.Walk and "deny" in Store.Put.
const NamespaceWildcard = "*"

// StagingPrefix is the driver path prefix for in-flight blob writes:
// a top-level ".staging" dir at the store root. A blob is written here
// until its content hash is known, then renamed to its final
// hash-derived path. The dotted top-level name keeps staging blobs out
// of the way of other engine code: the bootstrap Orphan Scan treats
// dangling staging files as orphans and prunes them. Both the write
// path (which creates these files) and the Orphan Scan (which reclaims
// them) reference this constant, so the convention is defined once here.
const StagingPrefix = ".staging"
