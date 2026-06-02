package errs

import "errors"

// Cryptography: passphrase derivation, KEK/DEK handling, recovery
// kit.

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

// ErrDescriptorSplitBrain — L0 and L1 are both valid, hold
// different content, and carry the same Sequence number. The
// engine cannot pick a winner without external input: a higher
// Sequence on either side would have made it the canonical
// version, but identical sequences mean two writers produced
// divergent descriptors with no causal order.
//
// Recovery is manual: the operator inspects both replicas and
// either picks one explicitly (overwriting both) or falls back
// to the Recovery Kit through RebuildIndexAgent.
var ErrDescriptorSplitBrain = errors.New("scrinium: descriptor split-brain (L0 and L1 diverged at the same sequence)")

// ErrPassphraseAlreadySet — Store.SetPassphrase was called on a
// Store whose DEK is already wrapped. The intended path for
// rotating the passphrase is RotateKEK, which proves possession
// of the current passphrase before accepting the new one.
//
// Returned only by SetPassphrase; the unwrap-then-wrap flow of
// RotateKEK does not surface it.
var ErrPassphraseAlreadySet = errors.New("scrinium: passphrase already set (use RotateKEK to change it)")
