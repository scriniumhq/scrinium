package descriptor

import "bytes"

// Equal reports whether a and b are field-by-field equivalent.
// nil-equal nil; non-nil-not-equal-to-nil; otherwise every field
// (including KDFParams sub-fields and the salt slice) must match.
//
// Note: Equal compares the in-memory representation, not the
// serialised form. Two descriptors that round-trip identically
// through Marshal/Unmarshal are Equal; the reverse holds because
// JSON encoding here is canonical (sorted-key, deterministic).
func Equal(a, b *Descriptor) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.StoreID != b.StoreID ||
		a.SchemaVersion != b.SchemaVersion ||
		a.Sequence != b.Sequence ||
		a.DEKEncrypted != b.DEKEncrypted {
		return false
	}
	if !bytes.Equal(a.DEK, b.DEK) {
		return false
	}
	switch {
	case a.KDFParams == nil && b.KDFParams == nil:
		return true
	case a.KDFParams == nil || b.KDFParams == nil:
		return false
	}
	ka, kb := a.KDFParams, b.KDFParams
	if ka.Algorithm != kb.Algorithm ||
		ka.Time != kb.Time ||
		ka.Memory != kb.Memory ||
		ka.Threads != kb.Threads {
		return false
	}
	return bytes.Equal(ka.Salt, kb.Salt)
}
