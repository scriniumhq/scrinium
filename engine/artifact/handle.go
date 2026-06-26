package artifact

// handle.go — external identity (ArtifactID) computation.

import (
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/hashing"
)

// identityMetaCanonEmpty is the canonical encoding of an empty identity
// partition — the v1 default (no fields are opted into identity). md =
// H(identityMetaCanonEmpty) is then a fixed token. The real canonical
// codec and the opt-in identity-field mechanism are deferred to the
// format-ADR; treat this marker as versioned and immutable.
var identityMetaCanonEmpty = []byte("scrinium/identity-meta/v1:{}")

// ComputeHandle computes the floating ArtifactID (handle) for a manifest
// and populates m.ArtifactID, m.IdentityMetaHash (md) and m.IdentityNonce.
//
// md = H(canon(identity-meta)); in v1 the identity partition is empty, so
// md is a fixed token. handle = H(nk ‖ cd ‖ md ‖ nonce) (hashing.Handle).
// nk is the naming key (hashing.NamingKeyPublic in Plain/Sealed); nonce is
// fresh 16 random bytes in IdentityMode=Unique, nil in Coalesced. The
// caller (store) generates the nonce.
//
// Call ComputeHandle BEFORE encoding: the handle is part of the body, and
// the ManifestDigest is then the hash of the body that already carries it.
func ComputeHandle(
	m domain.Manifest,
	hashAlgo string,
	registry domain.HashRegistry,
	nk []byte,
	nonce []byte,
) (domain.Manifest, error) {
	h, err := registry.NewHasher(hashAlgo)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("artifact: hasher: %w", err)
	}
	if _, err := h.Write(identityMetaCanonEmpty); err != nil {
		return domain.Manifest{}, err
	}
	md := registry.Format(hashAlgo, h.Sum(nil))

	handle, err := hashing.Handle(registry, hashAlgo, nk, m.ContentHash, md, nonce)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("artifact: handle: %w", err)
	}
	m.ArtifactID = handle
	m.IdentityMetaHash = md
	m.IdentityNonce = nonce
	return m, nil
}
