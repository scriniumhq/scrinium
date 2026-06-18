package store

import (
	"fmt"
	"sync"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/errs"
)

// crypto_state.go — the Store's mutable crypto material grouped with
// the single mutex that guards it.
//
// Pulling the trio (descriptor, DEK, passphrase provider) plus the
// derived key resolver out of *store into one type means the lock and
// exactly the fields it protects live together: no field guarded by
// the mutex sits outside this struct, and no unrelated field sits
// inside it. The data path no longer reaches for s.dek / s.cryptoMu
// directly — it goes through the narrow accessors below.
//
// Lock ordering (unchanged, still package-local and verifiable by
// reading this package):
//
//	cryptoState.mu  →  store.stateMu  →  store.cfgMu
//
// The admin crypto operations (unlockEncrypted, setPassphraseImpl,
// rotateKEKImpl, exportRecoveryKitImpl) hold mu for the whole
// operation and take store.stateMu briefly inside for the state
// transition — never the reverse. The data path never nests: Put and
// loadManifest take a one-shot guarded snapshot (dekForWrite /
// resolver) and release mu before doing any further work.
type cryptoState struct {
	mu sync.Mutex

	// desc is the current on-disk descriptor, kept in memory after
	// bootstrap so RotateKEK / SetPassphrase can produce a successor
	// (Sequence + 1, fresh KDFParams) without re-reading the Driver.
	desc *descriptor.Descriptor

	// dek is the unwrapped data-encryption key. nil for Plain Stores
	// and for encrypted Stores in StateLocked. Populated at a
	// successful Unlock; wiped and cleared when the Store returns to
	// Locked or is closed.
	dek []byte

	// provider is captured from WithPassphrase at construction and
	// kept for the Store's lifetime so later admin operations
	// (RotateKEK after a sleep, etc.) can re-prompt without the host
	// threading the provider through every call.
	provider domain.PassphraseProvider

	// keyResolver resolves DEKs on the read/write crypto paths.
	// Either injected via WithKeyResolver or promoted from dek by
	// promoteResolverIfDefault. Guarded by mu because Put and
	// loadManifest read it while promotion and Close mutate it.
	keyResolver pipeline.KeyResolver
}

// dekForWrite returns a private copy of the DEK for an encrypting
// write. The caller owns the copy and MUST wipe it (aead.Wipe) once
// the manifest is sealed. It errors if the Store is locked (no DEK)
// or has no key resolver — the two preconditions an encrypting
// ManifestCrypto needs. manifestCrypto is threaded through only so
// the error message names the offending mode.
func (c *cryptoState) dekForWrite(manifestCrypto domain.ManifestCrypto) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dek) == 0 {
		return nil, fmt.Errorf("%w: ManifestCrypto=%q requires Unlock", errs.ErrLocked, manifestCrypto)
	}
	if c.keyResolver == nil {
		return nil, fmt.Errorf("store.Put: ManifestCrypto=%q requires WithKeyResolver or default-resolver promotion", manifestCrypto)
	}
	return append([]byte{}, c.dek...), nil
}

// resolver returns the current key resolver under the lock. The
// resolver is an immutable interface value, so the snapshot is a
// pointer copy held only across the lock; a Locked Store returns nil,
// which the codec turns into the correct ErrKeyNotFound refusal.
func (c *cryptoState) resolver() pipeline.KeyResolver {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keyResolver
}

// promoteResolverIfDefault installs a default StaticKeyResolver over
// the DEK, only when no resolver was injected via WithKeyResolver
// (the discipline is "do not surprise the caller"). Idempotent.
// Caller must hold mu.
func (c *cryptoState) promoteResolverIfDefault() {
	if c.keyResolver != nil {
		return
	}
	if len(c.dek) == 0 {
		return
	}
	c.keyResolver = pipeline.NewStaticKeyResolver(c.dek)
}

// closeSecrets wipes and clears the DEK and returns the key resolver
// for the caller to Close outside the lock (a default
// StaticKeyResolver drops its own DEK copy on Close; a custom
// resolver is owned by the host and left untouched). Used by
// store.Close.
func (c *cryptoState) closeSecrets() pipeline.KeyResolver {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dek) > 0 {
		aead.Wipe(c.dek)
	}
	c.dek = nil
	return c.keyResolver
}
