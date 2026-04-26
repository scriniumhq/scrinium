package errs

import "errors"

// Cryptography: passphrase derivation, KEK/DEK handling, recovery
// kit. See docs/2. Internals/03 §3.1 for the key model,
// docs/2. Internals/10 §10.3 for the RecoveryKit format.

// ErrInvalidKey — the KEK does not decrypt the DEK: wrong password
// or corrupted EncryptedDEK.
var ErrInvalidKey = errors.New("scrinium: invalid key")

// ErrPassphraseRequired — the operation needs a KEK but
// WithPassphrase was not configured. Also returned by
// ExportRecoveryKit on a ManifestCrypto: Plain Store.
var ErrPassphraseRequired = errors.New("scrinium: passphrase required")

// ErrPassphraseProvider — the provider returned an error. Wraps
// the underlying cause (available through errors.Unwrap).
var ErrPassphraseProvider = errors.New("scrinium: passphrase provider error")

// ErrRecoveryKitCorrupted — the Recovery Kit is corrupted (the
// checksum does not match).
var ErrRecoveryKitCorrupted = errors.New("scrinium: recovery kit corrupted")

// ErrInvalidRecoveryKey — failed to decrypt the DEK from the
// Recovery Kit.
var ErrInvalidRecoveryKey = errors.New("scrinium: invalid recovery key")

// ErrKeyNotFound — the KeyResolver does not know the key for the
// requested KeyID.
var ErrKeyNotFound = errors.New("scrinium: key not found")

// ErrDecryptionFailed — the key was found but decryption failed
// (wrong key, corrupted bytes, authentication-tag failure).
var ErrDecryptionFailed = errors.New("scrinium: decryption failed")
