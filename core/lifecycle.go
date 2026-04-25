package core

import (
	"context"
	"errors"

	"github.com/rkurbatov/scrinium/driver"
)

// PassphraseHint is the call context for a PassphraseProvider.
// Reason takes the values "init", "unlock", or "kek_rotation"; with
// Reason = "init", StoreID carries the freshly generated UUID of the
// new Store.
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
	forceReinit     bool
	purgeOnReinit   bool
	cfg             *StoreConfig
	storeIndex      StoreIndex
	publisher       Publisher
	hashRegistry    HashRegistry
	readRegistry    TransformerRegistry
	keyResolver     KeyResolver
	passphrase      PassphraseProvider
	autoUnlock      bool
	capabilityToken []byte
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
// a divergence in immutable fields produces ErrConfigMismatch.
func WithConfig(cfg StoreConfig) StoreOption {
	return func(o *storeOptions) { o.cfg = &cfg }
}

// WithStoreIndex provides the StoreIndex implementation. Required.
func WithStoreIndex(idx StoreIndex) StoreOption {
	return func(o *storeOptions) { o.storeIndex = idx }
}

// WithPublisher provides a Publisher implementation for emitting
// events.
func WithPublisher(p Publisher) StoreOption {
	return func(o *storeOptions) { o.publisher = p }
}

// WithHashRegistry provides the registry of hash algorithms.
// Required. Used by the Pipeline runner, Recovery Agent, and
// parsers.
func WithHashRegistry(r HashRegistry) StoreOption {
	return func(o *storeOptions) { o.hashRegistry = r }
}

// WithReadRegistry provides the registry of transformation plugins.
// Required when StoreConfig.Pipeline is non-empty or
// MetadataTransformer is set.
func WithReadRegistry(r TransformerRegistry) StoreOption {
	return func(o *storeOptions) { o.readRegistry = r }
}

// WithKeyResolver provides the key-resolver plugin. By default the
// engine uses StaticKeyResolver populated with the DEK from
// store.json.
func WithKeyResolver(r KeyResolver) StoreOption {
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

// WithCapabilityToken provides a token for elevated permissions
// (such as access to system.* through WalkSystem).
func WithCapabilityToken(token []byte) StoreOption {
	return func(o *storeOptions) { o.capabilityToken = token }
}

// InitStore creates a new Store at the Location served by drv.
// It returns the open Store, the contents of the Recovery Kit as
// bytes (nil for ManifestCrypto: Plain without a passphrase), and
// an error.
//
// Detailed behaviour, steps, and validation: docs/2. Internals/10
// Recovery §10.1.1 and docs/4. API Reference/01 Lifecycle §1.1.
//
// Implementation lands in M1.
func InitStore(ctx context.Context, drv driver.Driver, opts ...StoreOption) (Store, []byte, error) {
	return nil, nil, errors.New("core.InitStore: not implemented")
}

// OpenStore opens an existing Store. It performs descriptor
// consensus, loads the active StoreConfig from
// system.config/current, validates the StoreIndex, and sets the
// final state of the Store.
//
// Detailed behaviour: docs/2. Internals/10 Recovery §10.1.2 and
// docs/4. API Reference/01 Lifecycle §1.2.
//
// Implementation lands in M1.
func OpenStore(ctx context.Context, drv driver.Driver, opts ...StoreOption) (Store, error) {
	return nil, errors.New("core.OpenStore: not implemented")
}
