package store

// Close releases secrets held by the Store. See the AdminStore.Close
// doc-comment for contract. Idempotent.
//
// Order of operations:
//  1. Mark closed under stateMu (early-return for repeat calls).
//     This is the gate any subsequent operation hits via
//     checkOperational, which short-circuits to os.ErrClosed.
//  2. Wipe DEK and capability token via crypto.closeSecrets —
//     long-lived secret material that does not survive shutdown.
//  3. If a default StaticKeyResolver was promoted, ask it to drop
//     its DEK copy. Custom resolvers are owned by the host and
//     are left untouched.
//
// Close does NOT transition state to Locked. "Closed" is its own
// terminal condition and surfaces as os.ErrClosed; "Locked" is
// reserved for an encrypted store before a successful Unlock.
// Conflating the two confused Plain-store users into searching
// for a passphrase that did not exist.
//
// Close does NOT close the StoreIndex — its lifetime belongs to the
// host (see WithStoreIndex doc).
func (s *store) Close() error {
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return nil
	}
	s.closed = true
	s.stateMu.Unlock()

	resolver := s.crypto.closeSecrets()

	if r, ok := resolver.(interface{ Close() }); ok {
		r.Close()
	}

	return nil
}
