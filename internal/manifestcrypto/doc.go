// Package manifestcrypto provides AES-256-GCM authenticated
// encryption for manifest bodies in MetadataOnly and Envelope
// modes per docs/2. Internals/03 §3.3.
//
// The package wraps Go's stdlib AES-GCM with a fixed-size DEK
// contract and a "nonce-prepended" output format identical in
// shape to core/internal/keywrap, but semantically distinct:
// keywrap protects the DEK with a KEK derived from a passphrase,
// manifestcrypto protects manifest bytes with the DEK itself.
//
// AAD usage: callers pass the manifest file header as additional
// authenticated data. AEAD-tag mismatch on Open then detects
// tampering with KeyID, encoding magic, or crypto flag — even
// before the engine recomputes ArtifactID. ArtifactID
// verification (§3.4) remains the primary tamper-evidence
// mechanism; AAD is the second line.
//
// DAG: manifestcrypto depends on errs and stdlib (crypto/aes,
// crypto/cipher, crypto/rand). It does not import core, domain,
// or any consumer.
package manifestcrypto
