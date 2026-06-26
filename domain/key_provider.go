package domain

// KeyProvider is the minimal slice of a key resolver that decryption needs:
// given a manifest's recorded KeyID, it returns the candidate DEKs to try
// (any live key decrypts — supports rotation/multi-DEK). It lives in domain,
// the leaf package, so consumers that only pass a provider through (e.g. the
// named layer's cell reader) need not import the artifact codec. The store's
// KeyResolver satisfies it implicitly; tests substitute a hand-rolled one.
// Implementations hand out defensive copies; callers wipe them.
type KeyProvider interface {
	GetKeys(keyID string) ([][]byte, error)
}
