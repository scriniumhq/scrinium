package core

import (
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/manifestcrypto"
)

// Close releases secrets held by the Store. See the AdminStore.Close
// doc-comment for contract. Idempotent.
//
// Order of operations:
//  1. Mark closed under stateMu (early-return for repeat calls).
//  2. Wipe DEK and capability token under cryptoMu — long-lived
//     secret material that does not survive shutdown.
//  3. If a default StaticKeyResolver was promoted, ask it to drop
//     its DEK copy. Custom resolvers are owned by the host and
//     are left untouched.
//  4. Transition state to Locked. For an encrypted Store this
//     mirrors the failed-Unlock path; for a Plain Store the
//     state is semantic only (no operations are gated on Locked
//     in Plain mode), but the transition keeps the closed/Locked
//     invariant uniform across Store kinds.
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
	s.state = domain.StateLocked
	s.stateMu.Unlock()

	s.cryptoMu.Lock()
	if len(s.dek) > 0 {
		manifestcrypto.Wipe(s.dek)
	}
	s.dek = nil
	if len(s.capabilityToken) > 0 {
		manifestcrypto.Wipe(s.capabilityToken)
	}
	s.capabilityToken = nil
	resolver := s.keyResolver
	s.cryptoMu.Unlock()

	if r, ok := resolver.(*staticKeyResolver); ok {
		r.close()
	}

	return nil
}
