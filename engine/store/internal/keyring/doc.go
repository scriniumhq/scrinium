// Package keyring is the engine's key-management layer: KEK
// derivation (Argon2id), DEK generation, and the wrap/unwrap of a
// DEK under a passphrase-derived KEK. It consolidates what used to
// be three separate pieces — the kdf and keywrap subpackages and
// the DEK orchestration in store/crypto.go — into one place, since
// they are only ever used together and only by the store layer.
//
// Layering: keyring sits below store and above the byte-level AEAD
// primitive (internal/manifestcrypto, used for wiping here). It
// does NOT touch *store state; the store's crypto-admin methods
// (Unlock/RotateKEK/SetPassphrase/ExportRecoveryKit) orchestrate
// these functions over store state.
//
// On-disk KDF parameters cross the boundary as descriptor.KDFParams
// (the persisted shape); the client-facing cost shape is
// domain.KDFParams.
package keyring
