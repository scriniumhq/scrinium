package domain

// StoreOwnership classifies a store_id recorded on a system artifact — a
// lease record, a system-artifact envelope, a checkpoint pointer — against
// the authoritative store_id of the store reading it. It is the shared
// shape behind every "does this artifact belong to my store?" check
// (ADR-104). It only classifies: the reaction to a mismatch is the
// consumer's. A lease reclaims a foreign slot (a leaked lease is not a
// conflict to wait out); a system-artifact read rejects a foreign or
// missing id as an identity error. The read paths differ too (a lease is
// read driver-direct before bootstrap; other system artifacts go through
// SystemStore), so only the comparison is centralised here.
type StoreOwnership int

const (
	// StoreOwnershipUnknown — one side carries no store_id: a lease with no
	// recorded owner (location.lock, or a lease written by pre-store_id
	// code), or a check run before the authoritative id is known. Never
	// treated as foreign, so an un-owned lease keeps its normal protection.
	StoreOwnershipUnknown StoreOwnership = iota
	// StoreOwnershipOwn — the recorded id matches the authoritative one.
	StoreOwnershipOwn
	// StoreOwnershipForeign — both ids are present and differ: the artifact
	// explicitly belongs to a different store.
	StoreOwnershipForeign
)

// ClassifyStoreOwnership compares a store_id recorded on an artifact
// against the authoritative store_id. An empty string on either side is
// Unknown (never Foreign), so a foreign artifact is only ever flagged on
// an explicit id mismatch — a lease without a store_id, or a check made
// before the descriptor is read, is left to the normal path rather than
// being reclaimed or rejected as someone else's.
func ClassifyStoreOwnership(recorded, authoritative string) StoreOwnership {
	if recorded == "" || authoritative == "" {
		return StoreOwnershipUnknown
	}
	if recorded == authoritative {
		return StoreOwnershipOwn
	}
	return StoreOwnershipForeign
}
