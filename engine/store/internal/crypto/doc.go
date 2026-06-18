// Package crypto is the store's self-contained crypto mechanics: it
// turns a passphrase into a wrapped DEK and a Recovery Kit, and back.
//
// It holds the pieces that depend only on the crypto primitives
// (internal/keyring for KEK derivation and DEK wrapping,
// internal/recoverykit for the kit format, internal/descriptor for the
// on-disk shape) and never on the store's state machine. The lifecycle
// ORCHESTRATION — when to prompt, how the state machine transitions, the
// bootstrap Orphan Scan — stays in package store, which calls these
// helpers. Pulling the mechanics out keeps the security-critical KDF and
// wrapping logic in one auditable place, separate from the locking and
// state-transition concerns that surround it.
package crypto
