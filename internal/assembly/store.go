package assembly

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"os"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// store.go — store-construction phases (openStore, composeWrappers) and the
// store/index helpers they rely on: open/init, policy→StoreConfig, the hash
// registry, the unsupported-policy guard, and the wrappedStore composite.

// openStore derives the StoreConfig and passphrase from policy (a
// WithPassphrase option wins), creates the shared event bus and attaches
// build-time handlers before anything publishes, then opens or initialises
// the store. It records the store rollback and fails fast on a locked store.
func (bs *buildState) openStore() error {
	// StoreConfig + passphrase from the policy.
	cfg, _ := storeConfigFromPolicy(bs.spec.Policy)
	pp, err := passphraseProvider(bs.ctx, bs.spec.Policy)
	if err != nil {
		return fmt.Errorf("scrinium: passphrase: %w", err)
	}
	if bs.opts.passphrase != nil {
		pp = bs.opts.passphrase // WithPassphrase: option takes precedence over policy
	}

	// Event bus: shared by the store and the agents the assembler wires, so
	// a host can subscribe to both through one channel. Build-time handlers
	// (WithEventHandler) are attached now, before anything publishes, so
	// they observe events emitted during assembly.
	bus := event.NewEventBus()
	for _, h := range bs.opts.eventHandlers {
		bus.Subscribe(h)
	}
	bs.bus = bus

	storeOpts := []store.StoreOption{
		store.WithStoreIndex(bs.idx),
		store.WithHashRegistry(hashRegistry()),
		store.WithConfig(cfg),
		store.WithPublisher(bus),
	}
	if pp != nil {
		storeOpts = append(storeOpts, store.WithPassphrase(pp), store.WithAutoUnlock())
	}

	st, created, kit, err := openOrInitStore(bs.ctx, bs.drv, bs.mode, storeOpts)
	if err != nil {
		return err
	}
	bs.st = st
	bs.created = created
	bs.kit = kit
	bs.cleanups = append(bs.cleanups, func() {
		if err := st.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium: store close on rollback: %v\n", err)
		}
	})
	if st.State() == domain.StateLocked {
		return fmt.Errorf("scrinium: store is locked; check the encryption passphrase")
	}
	return nil
}

// composeWrappers applies the behavior axis (Tier 3): the store's data
// plane is decorated by each extension wrapper innermost-first, then the
// full Store is preserved via a composite so administrative/maintenance
// operations pass through unchanged. The stack is validated (ADR-75) before
// composing. A no-wrapper build leaves the store untouched.
func (bs *buildState) composeWrappers() error {
	if len(bs.wrapFactories) == 0 {
		return nil
	}
	descs := make([]wrapper.Descriptor, len(bs.wrapFactories))
	for i, f := range bs.wrapFactories {
		descs[i] = f.Descriptor()
	}
	if verr := wrapper.Validate(descs, wrapper.ValidateOptions{}); verr != nil {
		return fmt.Errorf("scrinium: wrapper composition: %w", verr)
	}
	data := store.DataStore(bs.st)
	for _, f := range bs.wrapFactories {
		d, werr := f.Wrap(data, wrapper.Deps{Publisher: bs.bus})
		if werr != nil {
			return fmt.Errorf("scrinium: apply extension wrapper: %w", werr)
		}
		data = d
	}
	bs.st = wrappedStore{DataStore: data, AdminStore: bs.st}
	return nil
}

// guardUnsupportedPolicy rejects policy features whose components are
// not wired yet, with a precise pointer to the landing chunk.
func guardUnsupportedPolicy(p *Policy) error {
	if p == nil {
		return nil
	}
	switch {
	case p.Chunking != nil:
		return fmt.Errorf("scrinium: chunking is not wired yet (M5/C3): %w", errs.ErrNotImplemented)
	case p.Bundling != nil:
		return fmt.Errorf("scrinium: bundling is not wired yet (M4/S4): %w", errs.ErrNotImplemented)
	case len(p.Pipeline) > 0 || len(p.PipelineExtra) > 0:
		return fmt.Errorf("scrinium: explicit pipeline assembly is not wired yet: %w", errs.ErrNotImplemented)
	}
	return nil
}

// openOrInitStore opens or initialises the store per mode. It reports
// whether the store was freshly created and, for a fresh encrypted
// store, the recovery-kit bytes the host must persist (nil otherwise).
func openOrInitStore(
	ctx context.Context,
	drv driver.Driver,
	mode buildMode,
	opts []store.StoreOption,
) (st store.Store, created bool, kit []byte, err error) {
	switch mode {
	case modeOpen:
		st, err := store.OpenStore(ctx, drv, opts...)
		if err != nil {
			return nil, false, nil, fmt.Errorf("scrinium: open store: %w", err)
		}
		return st, false, nil, nil
	case modeInit:
		return initStore(ctx, drv, opts)
	case modeOpenOrInit:
		st, err := store.OpenStore(ctx, drv, opts...)
		if err == nil {
			return st, false, nil, nil
		}
		if !isNotFound(err) {
			return nil, false, nil, fmt.Errorf("scrinium: open store: %w", err)
		}
		return initStore(ctx, drv, opts)
	default:
		return nil, false, nil, fmt.Errorf("scrinium: unknown build mode %d", mode)
	}
}

// initStore creates a fresh store and surfaces the recovery kit. For an
// unencrypted store InitStore returns no kit (empty slice); for an
// encrypted one it returns the kit the host must persist out of band —
// the assembler hands it up through the Assembly (Info.Created +
// RecoveryKit).
func initStore(ctx context.Context, drv driver.Driver, opts []store.StoreOption) (store.Store, bool, []byte, error) {
	st, kit, err := store.InitStore(ctx, drv, opts...)
	if err != nil {
		return nil, false, nil, fmt.Errorf("scrinium: init store: %w", err)
	}
	return st, true, kit, nil
}

func isNotFound(err error) bool {
	return errors.Is(err, errs.ErrStoreNotFound)
}

// storeConfigFromPolicy maps a config policy onto a domain.StoreConfig.
// Returns whether the store is encrypted. A nil policy → zero config
// (engine defaults: Plain, no dedup).
func storeConfigFromPolicy(p *Policy) (domain.StoreConfig, bool) {
	var cfg domain.StoreConfig
	if p == nil {
		return cfg, false
	}

	encrypted := p.Encryption != nil
	if encrypted {
		switch p.Encryption.Mode {
		case "paranoid":
			cfg.ManifestCrypto = domain.ManifestCryptoParanoid
		default: // "sealed" (defaulted)
			cfg.ManifestCrypto = domain.ManifestCryptoSealed
		}
		switch p.Encryption.Dedup {
		case "convergent":
			cfg.EncryptedDedup = domain.EncryptedDedupConvergent
		default:
			cfg.EncryptedDedup = domain.EncryptedDedupDisabled
		}
		if p.Encryption.SegmentSize > 0 {
			cfg.SegmentSize = int(p.Encryption.SegmentSize.Int64())
		}
	}

	switch p.DeletionPolicy {
	case "free":
		cfg.DeletionPolicy = domain.DeletionPolicyFree
	case "retention":
		cfg.DeletionPolicy = domain.DeletionPolicyRetention
	case "noDelete":
		cfg.DeletionPolicy = domain.DeletionPolicyNoDelete
	}
	if p.Retention != 0 {
		cfg.RetentionPeriod = p.Retention.Std()
	}

	return cfg, encrypted
}

func hashRegistry() domain.HashRegistry {
	return hashing.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

// wrappedStore presents a full Store whose data plane is decorated by one
// or more behavior wrappers (Tier 3) while administrative and maintenance
// operations pass through to the underlying store unchanged. DataStore
// and AdminStore have disjoint method sets, so embedding both is
// unambiguous (engine/store/store.go).
type wrappedStore struct {
	store.DataStore
	store.AdminStore
}
