package store

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/keyring"
	"scrinium.dev/engine/store/internal/storeconfig"
)

// InitStore creates a new Store at the Location served by drv.
//
// Behaviour:
//  1. Probe the Driver for an existing descriptor. If one is
//     present and WithForceReinit is NOT set, return
//     errs.ErrStoreAlreadyExists.
//  2. With WithForceReinit, wipe the structural state — the
//     descriptor and the manifests/ directory. Existing blobs/
//     are NOT removed unless WithPurgeOnReinit is also set;
//     this lets a user start a fresh Store on top of orphan
//     blobs and let GC reclaim them.
//  3. Generate a fresh StoreID. Apply config defaults. Validate
//     immutable parameters.
//  4. Validate that a StoreIndex was provided via WithStoreIndex.
//     core does NOT open the index itself — the caller wires the
//     concrete implementation (sqlite.NewStore, in-memory, etc.)
//     and passes it as a dependency. This keeps core free of any
//     import dependency on index/* packages (DAG: core ← index).
//  5. Generate a 32-byte DEK from crypto/rand. The DEK is
//     generated unconditionally; encryption can be
//     turned on later through SetPassphrase without re-keying.
//  6. If WithPassphrase is configured, derive a KEK through
//     Argon2id and wrap the DEK with AES-256-GCM. The wrapped
//     DEK plus its KDFParams land in the descriptor.
//     Otherwise the DEK is stored in the descriptor in
//     plaintext — semantically honest: no passphrase, no
//     protection.
//  7. Write store.json (both replicas) and the L2 cache.
//  8. Construct the *store object in StateUnlocked and return.
//     For encrypted Stores, also return the Recovery Kit
//     bytes — the host MUST persist them before reporting
//     success to the user.
//
// The phases above map to helpers: prepareInitLocation (1–2),
// the DEK lifecycle inline (5–6, kept visible for auditability),
// and persistInitState (7). The shared construct+unlock tail
// (buildStore, unlockBootstrap) lives in lifecycle_construct.go.
//
// Recovery Kit:
//   - nil for Plain-DEK Stores (no encryption to recover).
//   - non-nil text bytes for encrypted Stores.
//
// Refusal cases:
//   - ManifestCrypto != Plain without WithPassphrase →
//     errs.ErrPassphraseRequired. An unprotected DEK plus
//     encrypted manifests is the worst-of-both-worlds shape:
//     anyone who reads store.json gets the keys to all the
//     manifests for free.
func InitStore(ctx context.Context, drv driver.Driver, opts ...StoreOption) (Store, []byte, error) {
	if drv == nil {
		return nil, nil, errors.New("store.InitStore: nil driver")
	}

	// wrap is the local error-prefix closure for this function, also
	// threaded into the phase helpers so every site reads
	// "store.InitStore: <stage>: %w" with a single definition.
	wrap := func(stage string, err error) error {
		if stage == "" {
			return fmt.Errorf("store.InitStore: %w", err)
		}
		return fmt.Errorf("store.InitStore: %s: %w", stage, err)
	}

	// --- Resolve options ---

	o := storeOptions{}
	for _, fn := range opts {
		fn(&o)
	}

	// Apply defaults to the requested config (the user may have
	// passed nothing, or only some fields).
	cfg := domain.StoreConfig{}
	if o.cfg != nil {
		cfg = *o.cfg
	}
	cfg = storeconfig.ApplyDefaults(cfg)
	if err := storeconfig.ValidateImmutable(cfg); err != nil {
		return nil, nil, wrap("invalid config", err)
	}

	// --- Probe for existing descriptor; honour WithForceReinit ---

	if err := prepareInitLocation(ctx, drv, o.forceReinit, wrap); err != nil {
		return nil, nil, err
	}

	// --- Validate the StoreIndex dependency ---
	//
	// core does not import any concrete index implementation. The
	// caller is expected to construct one (sqlite.NewStore,
	// in-memory, or any other) and pass it via WithStoreIndex.
	// This keeps the dependency graph one-way: core ← index.

	idx := o.storeIndex
	if idx == nil {
		return nil, nil, fmt.Errorf(
			"store.InitStore: WithStoreIndex is required (see DI Example)")
	}

	// --- Refuse encrypted-manifest configs without a passphrase ---
	//
	// Sealed and Paranoid only make sense alongside a
	// wrapped DEK: encrypting manifests against a plaintext key
	// stored in store.json provides no protection, just
	// operational pain. Caught here at InitStore so the user
	// sees the conflict before any disk I/O.
	if cfg.ManifestCrypto != domain.ManifestCryptoPlain && o.passphrase == nil {
		return nil, nil, fmt.Errorf("store.InitStore: %w: ManifestCrypto=%q requires WithPassphrase",
			errs.ErrPassphraseRequired, cfg.ManifestCrypto)
	}

	// --- Generate identity and DEK ---
	//
	// DEK is generated for every Store regardless of crypto
	// configuration. When WithPassphrase is set the DEK is
	// wrapped with the resulting KEK; otherwise it lives in the
	// descriptor in plaintext. In both branches dek is the in-memory
	// unwrapped value, kept alive on *store after construction. Its
	// lifetime is owned here: every error path below wipes it.

	storeID := uuid.NewString()

	dek, err := keyring.GenerateDEK()
	if err != nil {
		return nil, nil, wrap("", err)
	}

	desc := &descriptor.Descriptor{
		StoreID:       storeID,
		SchemaVersion: descriptor.CurrentSchemaVersion,
		Sequence:      1,
	}

	var kit []byte
	if o.passphrase != nil {
		wrapped, kdfParams, kitBytes, ierr := initEncryptedDEK(
			ctx, storeID, dek, o.passphrase, cfg.KDFParams)
		if ierr != nil {
			aead.Wipe(dek)
			return nil, nil, wrap("", ierr)
		}
		desc.DEK = wrapped
		desc.DEKEncrypted = true
		desc.KDFParams = &kdfParams
		kit = kitBytes
	} else {
		// Plaintext DEK path. The descriptor carries the raw key.
		// Validate enforces "DEKEncrypted=false ⇒ KDFParams
		// absent"; we don't fight that rule.
		desc.DEK = dek
		desc.DEKEncrypted = false
	}

	// --- Persist descriptor, L2 cache, and system.config ---

	if err := persistInitState(ctx, drv, idx, o.hashRegistry, cfg, desc, wrap); err != nil {
		aead.Wipe(dek)
		return nil, nil, err
	}

	// --- Construct *store ---

	s, err := buildStore(ctx, o, drv, idx, cfg, desc, dek)
	if err != nil {
		aead.Wipe(dek)
		return nil, nil, wrap("", err)
	}
	s.crypto.promoteResolverIfDefault()
	if err := unlockBootstrap(ctx, s, o.publisher); err != nil {
		aead.Wipe(dek)
		return nil, nil, wrap("", err)
	}
	return s, kit, nil
}

// prepareInitLocation probes the Driver for an existing descriptor and
// applies the WithForceReinit policy. With no descriptor present it is a
// no-op (the normal fresh-Location path). A present descriptor refuses
// with errs.ErrStoreAlreadyExists unless force is set; an unreadable one
// refuses with errs.ErrStoreCorrupted unless force is set. Under force,
// the well-known descriptor file is removed — blobs/ are left in place
// for GC.
func prepareInitLocation(ctx context.Context, drv driver.Driver, forceReinit bool, wrap func(string, error) error) error {
	existing, probeErr := descriptor.Read(ctx, drv)
	switch {
	case probeErr == nil:
		// Descriptor present.
		if !forceReinit {
			return fmt.Errorf("%w: storeId=%s",
				errs.ErrStoreAlreadyExists, existing.StoreID)
		}
		// Force reinit: clean up structural state. We stay
		// conservative — only the well-known files are touched.
		if err := drv.Remove(ctx, descriptor.Path); err != nil {
			return wrap("remove old descriptor", err)
		}
	case errors.Is(probeErr, os.ErrNotExist):
		// Fresh Location, the normal path.
	default:
		// The descriptor exists but is unreadable. Refuse to proceed
		// without WithForceReinit; the user must decide whether they
		// really want to clobber what is there.
		if !forceReinit {
			return fmt.Errorf("%w: descriptor present but unreadable: %v",
				errs.ErrStoreCorrupted, probeErr)
		}
		_ = drv.Remove(ctx, descriptor.Path)
	}
	return nil
}

// persistInitState writes the descriptor (both replicas), refreshes the
// L2 cache, and persists the active StoreConfig as system.config. A hash
// registry is required for the config write (system.config
// must be readable before the Store opens for users). The descriptor and
// cache are written first so a config-write failure still leaves a
// readable Store identity behind.
func persistInitState(ctx context.Context, drv driver.Driver, idx index.StoreIndex, hashes domain.HashRegistry, cfg domain.StoreConfig, desc *descriptor.Descriptor, wrap func(string, error) error) error {
	if err := descriptor.Persist(ctx, drv, desc); err != nil {
		return wrap("write descriptor", err)
	}
	if err := descriptor.Save(ctx, idx, desc); err != nil {
		return wrap("save L2 cache", err)
	}
	if hashes == nil {
		return fmt.Errorf(
			"store.InitStore: WithHashRegistry is required to persist system.config")
	}
	if _, err := storeconfig.Write(ctx, drv, configWriter(drv, idx, hashes), cfg); err != nil {
		return wrap("write system.config", err)
	}
	return nil
}
