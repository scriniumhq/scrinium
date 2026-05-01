// Package kdf is the Scrinium key-derivation primitive.
//
// Derive turns a passphrase plus a stored set of Argon2id parameters
// into a 32-byte key-encryption key (KEK), suitable for AES-256-GCM
// wrapping of the data-encryption key (DEK).
//
// The package owns three concerns:
//
//   - parameter defaults and the minimum-validity check enforced
//     across InitStore / Unlock / RotateKEK;
//   - cryptographically-secure salt generation;
//   - the Argon2id derivation itself.
//
// Everything else — DEK lifecycle, descriptor wrapping, recovery
// kit shape — lives outside this package. The boundary is narrow
// on purpose: this is the cryptographic kernel, and it should be
// auditable in isolation.
//
// DAG: kdf depends on domain (for KDFParams shape), errs (for
// sentinel errors), and golang.org/x/crypto/argon2. It does not
// import core, driver, plugin, or anything else.
package kdf
