package descriptor

import "bytes"

// Equal reports whether a and b are field-by-field equivalent,
// including KDFParams sub-fields and the salt slice. Two nils are
// equal; a nil and a non-nil are not.
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
