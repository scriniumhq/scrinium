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
//  3. If a default StaticKeyResolver was promoted, ask it to drop
//     its DEK copy. Custom resolvers are owned by the host and
//     are left untouched.
//
// Close does NOT transition state to Locked. "Closed" is its own
// terminal condition and surfaces as os.ErrClosed; "Locked" is
// reserved for an encrypted store before a successful Unlock.
// Conflating the two would send Plain-store users hunting for a
// passphrase that does not exist.
//
// Close does NOT close the StoreIndex — its lifetime belongs to the
// host (see WithStoreIndex doc).
func (a adminFacet) Close() error {
	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		return nil
	}
	a.closed = true
	a.stateMu.Unlock()

	resolver := a.crypto.CloseSecrets()

	if r, ok := resolver.(interface{ Close() }); ok {
		r.Close()
	}

	// Lock-free diagnostic trace (ADR-60): all locks are released and
	// secrets are wiped by this point. context.Background() because Close
	// takes no ctx; the record carries no cancellation semantics.
	a.componentLogger("store").LogAttrs(context.Background(), slog.LevelDebug,
		"store closed", storeIDAttr(a.core))

	return nil
}
