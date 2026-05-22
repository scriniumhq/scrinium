package store

import (
	"context"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/pipeline"
)

// PassphraseHint is the call context for a PassphraseProvider.
//
// Reason takes one of:
//
//   - "init"           — InitStore is generating a fresh Store and
//     needs the passphrase that will wrap the
//     just-generated DEK. StoreID carries the
//     freshly generated UUID.
//   - "unlock"         — OpenStore, Store.Unlock, or the first half
//     of Store.RotateKEK needs the current
//     passphrase to unwrap the DEK. Hosts that
//     cache passphrases in a keychain key off
//     this Reason for both unlock paths.
//   - "set_passphrase" — Store.SetPassphrase is wrapping a DEK that
//     is currently in plaintext. The provider
//     returns the NEW passphrase.
//   - "kek_rotation"   — the second half of Store.RotateKEK; the
//     provider returns the NEW passphrase that
//     will wrap the existing DEK.
type PassphraseHint struct {
	StoreID string
	Reason  string
}

// PassphraseProvider returns a passphrase used to derive the KEK
// through the KDF. The buffer is zeroed by the engine after the KEK
// has been derived.
type PassphraseProvider func(ctx context.Context, hint PassphraseHint) ([]byte, error)

// StoreOption is an option for the Store constructor. It applies to
// InitStore and OpenStore. The order in which options are passed is
// irrelevant.
type StoreOption func(*storeOptions)

// storeOptions is the internal aggregate of options. Not exported.
// Its concrete content is filled in starting in M1+; for M0 it is a
// placeholder for the constructor signatures.
type storeOptions struct {
	// Fields are populated in M1+ as the corresponding With*
	// functions are wired up.
	forceReinit   bool
	purgeOnReinit bool
	cfg           *domain.StoreConfig
	storeIndex    index.StoreIndex
	publisher     event.Publisher
	hashRegistry  domain.HashRegistry
	readRegistry  pipeline.TransformerRegistry
	keyResolver   pipeline.KeyResolver
	passphrase    PassphraseProvider
	autoUnlock    bool
}

// WithForceReinit allows InitStore to run on top of an existing
// Store (deleting L0, L1, the StoreIndex, and the manifests/
// directory). The operation is irreversible.
func WithForceReinit() StoreOption {
	return func(o *storeOptions) { o.forceReinit = true }
}

// WithPurgeOnReinit, in combination with WithForceReinit, also
// removes physical blobs (rather than leaving them as orphans for
// later GC).
func WithPurgeOnReinit() StoreOption {
	return func(o *storeOptions) { o.purgeOnReinit = true }
}

// WithConfig provides the Store configuration. At InitStore it
// fixes the immutable parameters. At OpenStore it is checked
// against the configuration loaded from system.config/current —
// a divergence in immutable fields produces errs.ErrConfigMismatch.
func WithConfig(cfg domain.StoreConfig) StoreOption {
	return func(o *storeOptions) { o.cfg = &cfg }
}

// WithStoreIndex provides the StoreIndex implementation. Required.
func WithStoreIndex(idx index.StoreIndex) StoreOption {
	return func(o *storeOptions) { o.storeIndex = idx }
}

// WithPublisher provides a Publisher implementation for emitting
// events.
func WithPublisher(p event.Publisher) StoreOption {
	return func(o *storeOptions) { o.publisher = p }
}

// WithHashRegistry provides the registry of hash algorithms.
// Required. Used by the Pipeline runner, Recovery Agent, and
// parsers.
func WithHashRegistry(r domain.HashRegistry) StoreOption {
	return func(o *storeOptions) { o.hashRegistry = r }
}

// WithReadRegistry provides the registry of transformation plugins.
// Required when StoreConfig.Pipeline is non-empty.
func WithReadRegistry(r pipeline.TransformerRegistry) StoreOption {
	return func(o *storeOptions) { o.readRegistry = r }
}

// WithKeyResolver provides the key-resolver plugin. By default the
// engine uses StaticKeyResolver populated with the DEK from
// store.json.
func WithKeyResolver(r pipeline.KeyResolver) StoreOption {
	return func(o *storeOptions) { o.keyResolver = r }
}

// WithPassphrase provides the KEK provider. Required when
// ManifestCrypto != Plain. With Plain it is ignored.
func WithPassphrase(provider PassphraseProvider) StoreOption {
	return func(o *storeOptions) { o.passphrase = provider }
}

// WithAutoUnlock instructs OpenStore to call Unlock automatically on
// an encrypted Store. Without this flag, OpenStore returns the Store
// in StateLocked.
func WithAutoUnlock() StoreOption {
	return func(o *storeOptions) { o.autoUnlock = true }
}
