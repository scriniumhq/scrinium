package store

import (
	"context"
	"log/slog"
)

// Close releases secrets held by the Store. See the AdminStore.Close
// doc-comment for contract. Idempotent.
//
// Order of operations:
//  1. Mark closed under stateMu (early-return for repeat calls).
//     This is the gate any subsequent operation hits via
//     checkOperational, which short-circuits to os.ErrClosed.
//  2. Wipe DEK and capability token via crypto.CloseSecrets —
//     long-lived secret material that does not survive shutdown.
//  3. If the resolver in force exposes a Close() method, call it so it
//     can drop its DEK copy. The default promoted StaticKeyResolver
//     does. A host-supplied resolver is closed only if it opts in by
//     exposing Close(); one that does not is left untouched. The
//     trigger is the presence of the method, not ownership — a custom
//     resolver that implements Close() WILL be closed here.
//
// Close does NOT transition state to Locked. "Closed" is its own
// terminal condition and surfaces as os.ErrClosed; "Locked" is
// reserved for an encrypted store before a successful Unlock.
// Conflating the two would send Plain-store users hunting for a
// passphrase that does not exist.
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

	resolver := s.crypto.CloseSecrets()

	if r, ok := resolver.(interface{ Close() }); ok {
		r.Close()
	}

	// Lock-free diagnostic trace (ADR-60): all locks are released and
	// secrets are wiped by this point. context.Background() because Close
	// takes no ctx; the record carries no cancellation semantics.
	s.componentLogger("store").LogAttrs(context.Background(), slog.LevelDebug,
		"store closed", storeIDAttr(s))

	return nil
}
