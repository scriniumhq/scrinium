package crypto

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/keyring"
	"scrinium.dev/errs"
)

// State is the store's mutable crypto material grouped with the single
// mutex that guards it. Pulling the trio (descriptor, DEK, passphrase
// provider) plus the derived key resolver out of *store into this type
// means the lock and exactly the fields it protects live together.
//
// The state-machine orchestration around these operations — the
// enterAdmin/enterWrite gates, the Locked↔Unlocked transitions, the
// bootstrap Orphan Scan — stays in package store, which calls the methods
// here. The split changes the lock discipline from the previous design:
// State.mu and the store's stateMu are no longer nested. Each method
// below takes State.mu, does its key-material work (and, for the
// descriptor-mutating ones, the persist) entirely under it, and releases
// it before the store touches stateMu. The store therefore never holds
// stateMu while reaching for State.mu, and vice-versa — the old
// "crypto.mu → stateMu" ordering is satisfied trivially because the two
// are never held at once.
//
// drv and index are held so SetPassphrase and RotateKEK can persist the
// successor descriptor (both replicas + L2 cache) under mu, which is what
// keeps a concurrent second SetPassphrase from observing the pre-wrap
// descriptor and double-wrapping.
type State struct {
	mu sync.Mutex

	desc        *descriptor.Descriptor
	dek         []byte
	provider    domain.PassphraseProvider
	keyResolver pipeline.KeyResolver

	drv   driver.Driver
	index index.StoreIndex
}

// New builds the crypto State. desc and dek are the bootstrap material
// (dek nil for an encrypted Store opened Locked); provider and
// keyResolver come from the store options; drv and index are needed to
// persist a re-wrapped descriptor.
func New(
	desc *descriptor.Descriptor,
	dek []byte,
	provider domain.PassphraseProvider,
	keyResolver pipeline.KeyResolver,
	drv driver.Driver,
	idx index.StoreIndex,
) *State {
	return &State{
		desc:        desc,
		dek:         dek,
		provider:    provider,
		keyResolver: keyResolver,
		drv:         drv,
		index:       idx,
	}
}

// DEKForWrite returns a private copy of the DEK for an encrypting write.
// The caller owns the copy and MUST wipe it (aead.Wipe) once the manifest
// is sealed. It errors if the Store is locked (no DEK) or has no key
// resolver — the two preconditions an encrypting ManifestCrypto needs.
// manifestCrypto is threaded through only so the error message names the
// offending mode.
func (s *State) DEKForWrite(manifestCrypto domain.ManifestCrypto) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dek) == 0 {
		return nil, fmt.Errorf("%w: ManifestCrypto=%q requires Unlock", errs.ErrLocked, manifestCrypto)
	}
	if s.keyResolver == nil {
		return nil, fmt.Errorf("store.Put: ManifestCrypto=%q requires WithKeyResolver or default-resolver promotion", manifestCrypto)
	}
	return append([]byte{}, s.dek...), nil
}

// Resolver returns the current key resolver under the lock. A Locked
// Store returns nil, which the codec turns into the correct
// ErrKeyNotFound refusal.
func (s *State) Resolver() pipeline.KeyResolver {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keyResolver
}

// KeyProvider returns the resolver adapted to an artifact.KeyProvider for
// decoding encrypted manifests read directly off the Driver (the rebuild
// agent's out-of-band path). Returns nil for an unencrypted Store. This
// is the sanctioned accessor; callers never reach into the resolver field
// directly.
func (s *State) KeyProvider() artifact.KeyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	return asKeyProvider(s.keyResolver)
}

// PromoteResolverIfDefault installs a default StaticKeyResolver over the
// DEK when no resolver was injected. Acquires the lock; safe to call from
// the construction paths (InitStore / OpenStore) after the DEK is set.
func (s *State) PromoteResolverIfDefault() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.promoteResolverIfDefault()
}

// promoteResolverIfDefault installs a default StaticKeyResolver over the
// DEK, only when no resolver was injected via WithKeyResolver (the
// discipline is "do not surprise the caller"). Idempotent. Caller must
// hold mu.
func (s *State) promoteResolverIfDefault() {
	if s.keyResolver != nil {
		return
	}
	if len(s.dek) == 0 {
		return
	}
	s.keyResolver = pipeline.NewStaticKeyResolver(s.dek)
}

// CloseSecrets wipes and clears the DEK and returns the key resolver for
// the caller to Close outside the lock (a default StaticKeyResolver drops
// its own DEK copy on Close; a custom resolver is owned by the host and
// left untouched). Used by store.Close.
func (s *State) CloseSecrets() pipeline.KeyResolver {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dek) > 0 {
		aead.Wipe(s.dek)
	}
	s.dek = nil
	return s.keyResolver
}

// WipeDEK wipes and clears the DEK. Used by the store's unlock path when
// the post-unwrap bootstrap fails and the key must not linger.
func (s *State) WipeDEK() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dek) > 0 {
		aead.Wipe(s.dek)
	}
	s.dek = nil
}

// HasDEK reports whether a DEK is currently held. It exposes only the
// presence bit, never the key material, so it is safe for tests and
// callers that need to assert lock/wipe state without reaching into the
// key bytes.
func (s *State) HasDEK() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dek) > 0
}

// UnlockDEK is the key-material leg of Store.Unlock: prompt with
// Reason="unlock", unwrap the DEK, and promote the default resolver. It
// is idempotent on the DEK — if one is already held it returns nil
// without prompting, so a racing second Unlock cannot double-prompt. It
// does NOT touch the state machine; the store wrapper owns the
// Locked→Bootstrapping→Unlocked transition and the Orphan Scan.
func (s *State) UnlockDEK(ctx context.Context, storeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dek) > 0 {
		return nil
	}
	// Plain DEK in Locked state would be a bug — Plain Stores open to
	// Unlocked unconditionally. Defensive surface; if we hit it, the
	// open-path invariant is broken.
	if s.desc == nil || !s.desc.DEKEncrypted || s.desc.KDFParams == nil {
		return fmt.Errorf("%w: descriptor in Locked state lacks crypto fields", errs.ErrStoreCorrupted)
	}
	passphrase, err := CallProvider(ctx, s.provider, domain.PassphraseHint{
		StoreID: storeID,
		Reason:  "unlock",
	})
	if err != nil {
		return err
	}
	dek, err := keyring.UnwrapDEK(s.desc.DEK, *s.desc.KDFParams, passphrase)
	aead.Wipe(passphrase)
	if err != nil {
		return err
	}
	s.dek = dek
	s.promoteResolverIfDefault()
	return nil
}

// SetPassphrase wraps a currently-plaintext DEK with a KEK derived from a
// fresh passphrase (Reason="set_passphrase"), persists the successor
// descriptor (Sequence+1), and adopts it — all under mu so a concurrent
// second call cannot observe the pre-wrap descriptor. cost is the KDF
// cost the caller resolved from config. The state gate (Unlocked, not
// Degraded) is enforced by the store wrapper before this is called.
//
// Refuses with ErrPassphraseAlreadySet when the DEK is already wrapped.
func (s *State) SetPassphrase(ctx context.Context, storeID string, cost domain.KDFParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.desc == nil {
		return fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if s.desc.DEKEncrypted {
		return errs.ErrPassphraseAlreadySet
	}
	if len(s.dek) == 0 {
		// A Plain Store must have a plaintext DEK after InitStore; if it
		// doesn't, the open-path invariant is broken.
		return fmt.Errorf("%w: Plain Store has no plaintext DEK", errs.ErrStoreCorrupted)
	}

	passphrase, err := CallProvider(ctx, s.provider, domain.PassphraseHint{
		StoreID: storeID,
		Reason:  "set_passphrase",
	})
	if err != nil {
		return err
	}
	wrapped, kdfParams, err := keyring.WrapDEK(s.dek, passphrase, cost)
	aead.Wipe(passphrase)
	if err != nil {
		return err
	}

	next := *s.desc // shallow copy is fine; we'll replace pointer fields
	next.Sequence = s.desc.Sequence + 1
	next.DEK = wrapped
	next.DEKEncrypted = true
	next.KDFParams = &kdfParams
	return s.commitDescriptor(ctx, &next)
}

// RotateKEK re-wraps the existing DEK under a new KEK, proven by first
// re-deriving and verifying the current passphrase (Reason="unlock") then
// prompting for the replacement (Reason="kek_rotation"). The DEK itself
// is unchanged, so rotation is O(1), not O(data). Persists and adopts the
// successor descriptor under mu. The state gate is enforced by the store
// wrapper.
func (s *State) RotateKEK(ctx context.Context, storeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.desc == nil {
		return fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if !s.desc.DEKEncrypted {
		return fmt.Errorf("%w: Store has plaintext DEK; use SetPassphrase", errs.ErrPassphraseRequired)
	}
	if s.desc.KDFParams == nil {
		return fmt.Errorf("%w: encrypted descriptor lacks KDFParams", errs.ErrStoreCorrupted)
	}

	// First half: prove possession of the current passphrase by
	// unwrapping the DEK and matching it against s.dek.
	currentPass, err := CallProvider(ctx, s.provider, domain.PassphraseHint{
		StoreID: storeID,
		Reason:  "unlock",
	})
	if err != nil {
		return fmt.Errorf("current passphrase: %w", err)
	}
	verified, err := keyring.UnwrapDEK(s.desc.DEK, *s.desc.KDFParams, currentPass)
	aead.Wipe(currentPass)
	if err != nil {
		return err
	}
	if !bytes.Equal(verified, s.dek) {
		aead.Wipe(verified)
		return fmt.Errorf("%w: current-passphrase unwrap produced unexpected DEK", errs.ErrStoreCorrupted)
	}
	aead.Wipe(verified)

	// Second half: obtain new passphrase, wrap with the same cost
	// parameters as before (rotation does not retune cost).
	newPass, err := CallProvider(ctx, s.provider, domain.PassphraseHint{
		StoreID: storeID,
		Reason:  "kek_rotation",
	})
	if err != nil {
		// CallProvider returns nil on error, so without this guard the
		// WrapDEK below sees an empty passphrase and returns
		// ErrPassphraseRequired, masking the provider's real failure.
		return fmt.Errorf("new passphrase: %w", err)
	}

	cost := domain.KDFParams{
		Time:    s.desc.KDFParams.Time,
		Memory:  s.desc.KDFParams.Memory,
		Threads: s.desc.KDFParams.Threads,
	}
	wrapped, kdfParams, err := keyring.WrapDEK(s.dek, newPass, cost)
	aead.Wipe(newPass)
	if err != nil {
		return err
	}

	next := *s.desc
	next.Sequence = s.desc.Sequence + 1
	next.DEK = wrapped
	next.KDFParams = &kdfParams
	// DEKEncrypted stays true; DEK bytes are only the wrapping change.
	return s.commitDescriptor(ctx, &next)
}

// RecoveryKit assembles the kit from the current descriptor and in-memory
// DEK. Stateless beyond reading them under the lock. The state gate
// (Unlocked or Degraded) is enforced by the store wrapper.
func (s *State) RecoveryKit() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.desc == nil {
		return nil, fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if !s.desc.DEKEncrypted {
		return nil, fmt.Errorf("%w: Plain Store has no Recovery Kit", errs.ErrPassphraseRequired)
	}
	return buildRecoveryKit(s.desc, s.desc.DEK)
}

// commitDescriptor persists the next descriptor (both replicas), refreshes
// the L2 cache, and atomically swaps s.desc. Caller must hold mu. On any
// error s.desc / s.dek are left unchanged so a retry re-derives from the
// same starting point.
func (s *State) commitDescriptor(ctx context.Context, next *descriptor.Descriptor) error {
	if err := descriptor.Persist(ctx, s.drv, next); err != nil {
		return fmt.Errorf("persist descriptor: %w", err)
	}
	if err := descriptor.Save(ctx, s.index, next); err != nil {
		// Persisted on disk but cache write failed. The next OpenStore
		// will rebuild the cache from Location, so this is recoverable;
		// surface the error so the caller knows it was not fully done.
		return fmt.Errorf("save L2 cache: %w", err)
	}
	s.desc = next
	return nil
}

// asKeyProvider adapts a pipeline.KeyResolver to an artifact.KeyProvider,
// mapping a nil resolver to a nil provider (the codec turns that into the
// correct ErrKeyNotFound refusal). pipeline.KeyResolver satisfies
// artifact.KeyProvider structurally (GetKeys), so this only nil-guards
// and forwards.
func asKeyProvider(r pipeline.KeyResolver) artifact.KeyProvider {
	if r == nil {
		return nil
	}
	return r
}
