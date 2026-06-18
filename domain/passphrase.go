package domain

import "context"

// PassphraseHint is the call context handed to a PassphraseProvider so a
// host can tailor its prompt (or key a keychain lookup) to the operation
// in flight. Reason is one of:
//   - "init"           — InitStore is encrypting a new Store; the provider
//     returns the passphrase that will wrap a freshly generated DEK.
//   - "unlock"         — OpenStore, Store.Unlock, or the first half of
//     Store.RotateKEK needs the current passphrase to unwrap the DEK.
//     Hosts that cache passphrases in a keychain key off this Reason for
//     both unlock paths.
//   - "set_passphrase" — Store.SetPassphrase is wrapping a DEK that is
//     currently in plaintext. The provider returns the NEW passphrase.
//   - "kek_rotation"   — the second half of Store.RotateKEK; the provider
//     returns the NEW passphrase that will wrap the existing DEK.
//
// It is part of the engine's public crypto vocabulary (alongside
// KDFParams and ManifestCrypto), which is why it lives in domain rather
// than in the store package: both the public WithPassphrase option and
// the internal crypto state reference it, and a shared leaf package is
// the only home that lets both without an import cycle.
type PassphraseHint struct {
	StoreID string
	Reason  string
}

// PassphraseProvider returns a passphrase used to derive the KEK through
// the KDF. The buffer is zeroed by the engine after the KEK has been
// derived; a provider must not retain or reuse it.
type PassphraseProvider func(ctx context.Context, hint PassphraseHint) ([]byte, error)
