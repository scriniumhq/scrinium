// Package keyring is the engine's key-management layer: KEK
// derivation (Argon2id), DEK generation, and the wrap/unwrap of a DEK
// under a passphrase-derived KEK. The three are only ever used
// together and only by the store layer, so they live in one package.
//
// keyring sits below the store and above the AEAD primitive
// (internal/aead, which supplies the AES-GCM construction and Wipe).
// It does not touch *store state; the store's crypto-admin methods
// (Unlock/RotateKEK/SetPassphrase/ExportRecoveryKit) orchestrate these
// functions over that state.
//
// On-disk KDF parameters cross the boundary as descriptor.KDFParams;
// the client-facing cost shape is config.KDFParams.
package keyring
