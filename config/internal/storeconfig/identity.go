package storeconfig

// IdentityMode is an immutable Store property controlling whether
// identical content+identity-meta coalesce to one handle.
//
//   - IdentityModeUnique (default): a fresh per-Put nonce is mixed
//     into the handle, so every Put yields a distinct ArtifactID.
//     WithIdempotent() opts a single call back into coalescing.
//   - IdentityModeCoalesced (WORM archive): no nonce; the handle is
//     deterministic (PRF(NK, cd‖md)), so identical artifacts share
//     one ArtifactID. WithUnique() opts a single call back out.
//
// Coalescing implies WORM (no deletion): a deduplicated manifest is
// referenced by the outside world invisibly to the store, so refcount
// is impossible. Coalescing is forbidden in Paranoid (a deterministic
// handle would leak content equality).
type IdentityMode string

const (
	IdentityModeUnique    IdentityMode = "unique"
	IdentityModeCoalesced IdentityMode = "coalesced"
)
