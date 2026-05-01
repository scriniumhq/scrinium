package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/core/internal/recoverykit"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/errs"
)

// PassphraseHint is the call context for a PassphraseProvider.
//
// Reason takes one of:
//
//   - "init"             — InitStore is generating a fresh Store
//     and needs the passphrase that will wrap
//     the just-generated DEK. StoreID carries
//     the freshly generated UUID.
//   - "unlock"           — OpenStore (or Store.Unlock) needs the
//     current passphrase to unwrap the DEK.
//   - "set_passphrase"   — Store.SetPassphrase is wrapping a DEK
//     that is currently in plaintext. The
//     provider returns the NEW passphrase.
//   - "kek_rotation"     — Store.RotateKEK is replacing the wrap.
//     The provider is called TWICE: first with
//     NeedNew=false to get the current
//     passphrase, then NeedNew=true to get the
//     replacement.
//
// NeedNew distinguishes the two halves of "kek_rotation". For all
// other Reason values it is false.
type PassphraseHint struct {
	StoreID string
	Reason  string
	NeedNew bool
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
	cfg             *domain.StoreConfig
	storeIndex      StoreIndex
	publisher       Publisher
	hashRegistry    domain.HashRegistry
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
// a divergence in immutable fields produces errs.ErrConfigMismatch.
func WithConfig(cfg domain.StoreConfig) StoreOption {
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
func WithHashRegistry(r domain.HashRegistry) StoreOption {
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
//     generated unconditionally per §3.1; encryption can be
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
// Recovery Kit:
//   - nil for Plain-DEK Stores (no encryption to recover).
//   - non-nil text bytes per §10.3 for encrypted Stores.
//
// Refusal cases:
//   - ManifestCrypto != Plain without WithPassphrase →
//     errs.ErrPassphraseRequired. An unprotected DEK plus
//     encrypted manifests is the worst-of-both-worlds shape:
//     anyone who reads store.json gets the keys to all the
//     manifests for free.
func InitStore(ctx context.Context, drv driver.Driver, opts ...StoreOption) (Store, []byte, error) {
	if drv == nil {
		return nil, nil, errors.New("core.InitStore: nil driver")
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
	cfg = applyConfigDefaults(cfg)
	if err := validateImmutableConfig(cfg); err != nil {
		return nil, nil, fmt.Errorf("core.InitStore: invalid config: %w", err)
	}

	// --- Probe for existing descriptor ---

	existing, probeErr := descriptor.Read(ctx, drv)
	switch {
	case probeErr == nil:
		// Descriptor present.
		if !o.forceReinit {
			return nil, nil, fmt.Errorf("%w: storeId=%s",
				errs.ErrStoreAlreadyExists, existing.StoreID)
		}
		// Force reinit: clean up structural state. We stay
		// conservative — only the well-known files are touched.
		// blobs/ stay in place unless purge is also requested
		// (purge wiring lands in M3 alongside the GC; M1.4 just
		// honours WithForceReinit for descriptor + index).
		if err := drv.Remove(ctx, descriptor.Path); err != nil {
			return nil, nil, fmt.Errorf("core.InitStore: remove old descriptor: %w", err)
		}
	case errors.Is(probeErr, os.ErrNotExist):
		// Fresh Location, the normal path.
	default:
		// The descriptor exists but is unreadable. Refuse to
		// proceed without WithForceReinit; the user must decide
		// whether they really want to clobber what is there.
		if !o.forceReinit {
			return nil, nil, fmt.Errorf("%w: descriptor present but unreadable: %v",
				errs.ErrStoreCorrupted, probeErr)
		}
		_ = drv.Remove(ctx, descriptor.Path)
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
			"core.InitStore: WithStoreIndex is required (see DI Example)")
	}

	// --- Refuse encrypted-manifest configs without a passphrase ---
	//
	// MetadataOnly and Envelope only make sense alongside a
	// wrapped DEK: encrypting manifests against a plaintext key
	// stored in store.json provides no protection, just
	// operational pain. Caught here at InitStore so the user
	// sees the conflict before any disk I/O.
	if cfg.ManifestCrypto != domain.ManifestCryptoPlain && o.passphrase == nil {
		return nil, nil, fmt.Errorf("core.InitStore: %w: ManifestCrypto=%q requires WithPassphrase",
			errs.ErrPassphraseRequired, cfg.ManifestCrypto)
	}

	// --- Generate identity ---

	storeID, err := generateUUID()
	if err != nil {
		// idx came from the caller via WithStoreIndex — we do not
		// own its lifecycle and must not close it on our error path.
		return nil, nil, err
	}

	// --- DEK lifecycle ---
	//
	// DEK is generated for every Store regardless of crypto
	// configuration (§3.1). When WithPassphrase is set the DEK
	// is wrapped with the resulting KEK; otherwise it lives in
	// the descriptor in plaintext.
	//
	// In both branches dek is the in-memory unwrapped value, kept
	// alive on *store after construction so subsequent writes
	// have it without re-fetching from descriptor.

	dek, err := generateDEK()
	if err != nil {
		return nil, nil, fmt.Errorf("core.InitStore: %w", err)
	}

	desc := &descriptor.Descriptor{
		StoreID:       storeID,
		SchemaVersion: descriptor.CurrentSchemaVersion,
		Sequence:      1,
	}

	var kit []byte
	if o.passphrase != nil {
		// Encrypted DEK path. Caller will receive the Recovery
		// Kit and is responsible for persisting it before
		// considering the Store usable.
		passphrase, perr := callProvider(ctx, o.passphrase, PassphraseHint{
			StoreID: storeID,
			Reason:  "init",
		})
		if perr != nil {
			return nil, nil, fmt.Errorf("core.InitStore: %w", perr)
		}

		// cfg.KDFParams is the client-side cost override; nil
		// means "use kdf.Default()". wrapDEK handles the zero
		// value; we don't need to dereference here.
		var cost domain.KDFParams
		if cfg.KDFParams != nil {
			cost = *cfg.KDFParams
		}
		wrapped, kdfParams, werr := wrapDEK(dek, passphrase, cost)
		zeroBytes(passphrase)
		if werr != nil {
			zeroBytes(dek)
			return nil, nil, fmt.Errorf("core.InitStore: wrap DEK: %w", werr)
		}

		desc.DEK = wrapped
		desc.DEKEncrypted = true
		desc.KDFParams = &kdfParams

		// Build the Recovery Kit before any disk I/O so a kit-
		// generation failure refuses to create the Store.
		k, kerr := buildRecoveryKit(desc, wrapped)
		if kerr != nil {
			zeroBytes(dek)
			return nil, nil, fmt.Errorf("core.InitStore: build recovery kit: %w", kerr)
		}
		kit = k
	} else {
		// Plaintext DEK path. The descriptor carries the raw key.
		// Validate enforces "DEKEncrypted=false ⇒ KDFParams
		// absent"; we don't fight that rule.
		desc.DEK = dek
		desc.DEKEncrypted = false
	}

	if err := descriptor.Persist(ctx, drv, desc); err != nil {
		zeroBytes(dek)
		return nil, nil, fmt.Errorf("core.InitStore: write descriptor: %w", err)
	}
	if err := saveDescriptorCache(idx, desc); err != nil {
		zeroBytes(dek)
		return nil, nil, fmt.Errorf("core.InitStore: save L2 cache: %w", err)
	}

	// --- Persist the active StoreConfig as system.config ---
	//
	// Per §10.1.4 system.config/current is the source of truth for
	// projection parameters. It must be writable before the Store
	// is open for users — Hash registry is therefore required.
	if o.hashRegistry == nil {
		return nil, nil, fmt.Errorf(
			"core.InitStore: WithHashRegistry is required to persist system.config")
	}
	if _, err := writeSystemConfig(ctx, drv, idx, o.hashRegistry, cfg); err != nil {
		return nil, nil, fmt.Errorf("core.InitStore: write system.config: %w", err)
	}

	// --- Construct *store ---

	s, err := buildStore(ctx, o, drv, idx, cfg, desc, dek)
	if err != nil {
		zeroBytes(dek)
		return nil, nil, fmt.Errorf("core.InitStore: %w", err)
	}
	if err := unlockBootstrap(ctx, s, o.publisher); err != nil {
		zeroBytes(dek)
		return nil, nil, fmt.Errorf("core.InitStore: %w", err)
	}
	return s, kit, nil
}

// OpenStore opens an existing Store at the Location served by drv.
//
// Behaviour:
//  1. Read both descriptor replicas (L0 store.json, L1
//     .store.backup.json) per §10.1.5 and reconcile them. If
//     both are absent → errs.ErrStoreNotFound. If both are
//     unrecoverable (corrupted, or one corrupted + one absent)
//     → errs.ErrStoreCorrupted. If both are valid but content
//     differs at the same Sequence →
//     errs.ErrDescriptorSplitBrain. Otherwise the canonical
//     replica is selected (sequence-wins; equal-content
//     short-circuit) and any heal action is performed against
//     the Driver before proceeding.
//  2. Reconcile the L2 cache (store_meta) with the canonical
//     descriptor. The cache is rewritten when absent, when its
//     checksum diverges, or when its load fails — Location is
//     the source of truth, the cache is a fast-start aid.
//  3. Validate that a StoreIndex was provided via WithStoreIndex.
//     core never opens an index itself; the caller is responsible
//     for the dependency. Missing → error.
//  4. Load the active StoreConfig from system.config/current.
//     Defaults are applied; immutable parameters are validated.
//  5. Validate WithConfig (when supplied) against the active
//     config: immutable mismatch → errs.ErrConfigMismatch.
//     A caller without WithConfig accepts the on-disk config
//     as-is — a legitimate scenario for diagnostic tools and
//     projection-only consumers.
//  6. State machine and bootstrap:
//     - DEKEncrypted=false: Plain DEK in descriptor, used
//     directly. Run Orphan Scan, transition to
//     StateUnlocked.
//     - DEKEncrypted=true + WithAutoUnlock: invoke the
//     configured PassphraseProvider with Reason="unlock",
//     unwrap DEK, run Orphan Scan, transition to
//     StateUnlocked.
//     - DEKEncrypted=true without WithAutoUnlock: skip
//     Orphan Scan (the index walk needs the DEK in M2.3+
//     for Envelope manifests; until then it would still
//     succeed for Plain manifests, but we treat the state
//     uniformly), transition to StateLocked. The next
//     Store.Unlock call completes bootstrap.
//
// MetadataOnly and Envelope manifest crypto are still rejected
// pending M2.3. The split between "encrypted DEK" (this pack)
// and "encrypted manifests" (next milestone) is deliberate:
// they are independent axes of the configuration and a Store
// with a passphrase-protected DEK plus Plain manifests is a
// useful intermediate.
//
// What does NOT happen yet (planned milestones in parens):
//   - location.lock acquisition / lease model (M3.1).
//   - StoreIndex schema cross-check against descriptor (M3.4).
func OpenStore(ctx context.Context, drv driver.Driver, opts ...StoreOption) (Store, error) {
	if drv == nil {
		return nil, errors.New("core.OpenStore: nil driver")
	}

	// --- Resolve options ---

	o := storeOptions{}
	for _, fn := range opts {
		fn(&o)
	}

	// --- StoreIndex dependency ---
	//
	// Required up-front: readSystemConfig and the L2 cache need
	// it; refusing here gives a clearer error than failing later.

	idx := o.storeIndex
	if idx == nil {
		return nil, fmt.Errorf(
			"core.OpenStore: WithStoreIndex is required (see DI Example)")
	}
	if o.hashRegistry == nil {
		return nil, fmt.Errorf(
			"core.OpenStore: WithHashRegistry is required to read system.config")
	}

	// --- Read both descriptor replicas; reconcile ---

	l0, l1, l0s, l1s, err := descriptor.ReadBoth(ctx, drv)
	if err != nil {
		// Non-recoverable I/O error from the Driver — propagate.
		return nil, fmt.Errorf("core.OpenStore: read descriptor: %w", err)
	}
	rec, err := descriptor.Reconcile(l0, l0s, l1, l1s)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, errs.ErrStoreNotFound
	case errors.Is(err, errs.ErrStoreCorrupted):
		return nil, errs.ErrStoreCorrupted
	case err != nil:
		// Split-brain, malformed-replica branches — pass through
		// unchanged so callers can identify by errors.Is.
		return nil, fmt.Errorf("core.OpenStore: %w", err)
	}
	desc := rec.Canonical

	// --- Heal divergent replicas ---
	//
	// Reconcile selected the canonical descriptor; if the on-disk
	// state diverges from it, write the side that needs updating.
	// Each heal is a single atomic Put; a crash mid-heal leaves
	// the same divergence we are recovering from now, and the
	// next OpenStore re-applies the same step. Idempotent.
	if err := healReplicas(ctx, drv, desc, rec.Action); err != nil {
		return nil, fmt.Errorf("core.OpenStore: %w", err)
	}

	// --- Reconcile L2 cache with canonical descriptor ---
	//
	// Location is the source of truth; the cache is a fast-start
	// aid only. Save when absent, when corrupted (load returned
	// an error), or when checksum diverges. Read errors are
	// non-fatal — we always have the canonical to fall back to.
	if err := refreshDescriptorCache(idx, desc); err != nil {
		return nil, fmt.Errorf("core.OpenStore: refresh L2 cache: %w", err)
	}

	// --- Load the active StoreConfig from system.config/current ---

	active, err := readSystemConfig(ctx, drv, o.hashRegistry)
	if err != nil {
		return nil, fmt.Errorf("core.OpenStore: read system.config: %w", err)
	}
	active = applyConfigDefaults(active)
	if err := validateImmutableConfig(active); err != nil {
		return nil, fmt.Errorf("%w: system.config produced invalid config: %v",
			errs.ErrStoreCorrupted, err)
	}

	// --- Validate WithConfig against the active config ---

	if o.cfg != nil {
		if err := validateAgainstActiveConfig(*o.cfg, active); err != nil {
			return nil, err
		}
	}

	// --- Refuse manifest-encryption modes still pending M2.3 ---
	//
	// MetadataOnly and Envelope require the AEAD-on-manifests
	// pipeline that lands with the next milestone. The DEK
	// machinery itself (this pack) is independent: a Store can
	// have a wrapped DEK and still write Plain manifests, which
	// is a perfectly working configuration.
	if active.ManifestCrypto != domain.ManifestCryptoPlain {
		return nil, fmt.Errorf(
			"core.OpenStore: ManifestCrypto=%q is not yet implemented (lands in M2.3)",
			active.ManifestCrypto)
	}

	// --- Branch on DEK protection state ---

	if !desc.DEKEncrypted {
		// Plain DEK: descriptor carries the raw key. Bootstrap
		// proceeds straight to Unlocked.
		s, err := buildStore(ctx, o, drv, idx, active, desc, desc.DEK)
		if err != nil {
			return nil, fmt.Errorf("core.OpenStore: %w", err)
		}
		if err := unlockBootstrap(ctx, s, o.publisher); err != nil {
			return nil, fmt.Errorf("core.OpenStore: %w", err)
		}
		return s, nil
	}

	// Encrypted DEK below.

	if !o.autoUnlock {
		// Stop in StateLocked — caller will follow up with
		// Store.Unlock. Orphan Scan is deferred until DEK is
		// available so the locked Store is a strictly minimal
		// in-memory shell.
		s, err := buildStore(ctx, o, drv, idx, active, desc, nil /* dek */)
		if err != nil {
			return nil, fmt.Errorf("core.OpenStore: %w", err)
		}
		s.stateMu.Lock()
		s.state = domain.StateLocked
		s.stateMu.Unlock()
		return s, nil
	}

	// AutoUnlock: invoke the provider, derive KEK, unwrap DEK.
	if o.passphrase == nil {
		return nil, fmt.Errorf("core.OpenStore: %w: WithAutoUnlock requires WithPassphrase",
			errs.ErrPassphraseRequired)
	}
	passphrase, err := callProvider(ctx, o.passphrase, PassphraseHint{
		StoreID: desc.StoreID,
		Reason:  "unlock",
	})
	if err != nil {
		return nil, fmt.Errorf("core.OpenStore: %w", err)
	}
	if desc.KDFParams == nil {
		// Defensive: a descriptor with DEKEncrypted=true must
		// have KDFParams (Validate enforces). Reaching this
		// branch means the Validate contract has drifted.
		zeroBytes(passphrase)
		return nil, fmt.Errorf("%w: descriptor reports DEKEncrypted=true without KDFParams",
			errs.ErrStoreCorrupted)
	}
	dek, err := unwrapDEK(desc.DEK, *desc.KDFParams, passphrase)
	zeroBytes(passphrase)
	if err != nil {
		return nil, fmt.Errorf("core.OpenStore: %w", err)
	}

	s, err := buildStore(ctx, o, drv, idx, active, desc, dek)
	if err != nil {
		zeroBytes(dek)
		return nil, fmt.Errorf("core.OpenStore: %w", err)
	}
	if err := unlockBootstrap(ctx, s, o.publisher); err != nil {
		return nil, fmt.Errorf("core.OpenStore: %w", err)
	}
	return s, nil
}

// buildRecoveryKit assembles the kit text for a freshly-encrypted
// descriptor. Called at InitStore (and later RotateKEK /
// SetPassphrase) before disk I/O — a kit-build failure aborts
// the Store creation rather than producing a Store the host
// cannot recover.
func buildRecoveryKit(desc *descriptor.Descriptor, wrappedDEK []byte) ([]byte, error) {
	if desc.KDFParams == nil {
		return nil, errors.New("buildRecoveryKit: descriptor missing KDFParams")
	}
	return recoverykit.Encode(recoverykit.Kit{
		StoreID:      desc.StoreID,
		CreatedAt:    time.Now().UTC(),
		Algorithm:    desc.KDFParams.Algorithm,
		Salt:         desc.KDFParams.Salt,
		Time:         desc.KDFParams.Time,
		Memory:       desc.KDFParams.Memory,
		Threads:      desc.KDFParams.Threads,
		EncryptedDEK: wrappedDEK,
	})
}

// healReplicas applies Reconcile's repair action: writes the
// damaged or missing replica from the canonical descriptor.
// HealNone is a no-op; the four healing actions reduce to two
// distinct disk operations (write L0 only, write L1 only) since
// the canonical content already lives on the surviving side.
func healReplicas(ctx context.Context, drv driver.Driver, canonical *descriptor.Descriptor, action descriptor.ReconcileAction) error {
	switch action {
	case descriptor.HealNone:
		return nil
	case descriptor.HealL0FromL1, descriptor.HealBothFromL1:
		// HealL0FromL1: L0 was missing/corrupted, rewrite it.
		// HealBothFromL1: sequence-divergence, L1 won, rewrite L0.
		// Same disk operation; distinct names preserve diagnostic
		// detail in logs.
		return descriptor.WriteReplica(ctx, drv, canonical, descriptor.L0)
	case descriptor.HealL1FromL0, descriptor.HealBothFromL0:
		return descriptor.WriteReplica(ctx, drv, canonical, descriptor.L1)
	default:
		return fmt.Errorf("core: unknown ReconcileAction %d", int(action))
	}
}

// refreshDescriptorCache compares the L2 cache against canonical
// and rewrites it when out of sync.
//
// Three branches that all reduce to "save":
//   - cache absent (loadDescriptorCache returned nil, nil)
//   - cache load failed (corruption, partial state)
//   - cache present but checksum diverges from canonical
//
// The "load failed" branch swallows the load error on purpose:
// the cache is a fast-start aid, not authoritative, and a
// damaged cache is fully recoverable from Location.
func refreshDescriptorCache(idx metaStore, canonical *descriptor.Descriptor) error {
	cache, _ := loadDescriptorCache(idx)

	if cache != nil {
		want, err := descriptor.Checksum(canonical)
		if err != nil {
			return fmt.Errorf("checksum canonical: %w", err)
		}
		if bytes.Equal(cache.Checksum, want) {
			return nil // cache is already current
		}
	}

	// Save (or re-save). saveDescriptorCache is idempotent.
	if err := saveDescriptorCache(idx, canonical); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	return nil
}

// buildStore is the common tail shared by InitStore and OpenStore.
// It constructs the *store value, runs the bootstrap Orphan Scan,
// publishes the report, and transitions the Store into
// StateUnlocked. Errors are surfaced unwrapped — the caller adds
// its own "core.InitStore" / "core.OpenStore" prefix.
//
// Pre-conditions checked by the caller (not re-checked here):
//   - drv != nil
//   - idx != nil
//   - cfg has been defaulted and validated
//   - storeID is fresh (Init) or read from the descriptor (Open)
//
// When M2 lands the Locked → Bootstrapping → Unlocked transition
// (encrypted Stores), this helper is the single point that learns
// to wait for Unlock before flipping the state — both entry
// points then pick up the new flow without further changes.
func buildStore(
	ctx context.Context,
	o storeOptions,
	drv driver.Driver,
	idx StoreIndex,
	cfg domain.StoreConfig,
	desc *descriptor.Descriptor,
	dek []byte,
) (*store, error) {
	_ = ctx // reserved for future bootstrap-time index probes
	s := &store{
		storeID:            desc.StoreID,
		drv:                drv,
		index:              idx,
		pub:                o.publisher,
		activeConfig:       cfg,
		state:              domain.StateBootstrapping,
		hashes:             o.hashRegistry,
		transformers:       o.readRegistry,
		keyResolver:        o.keyResolver,
		capabilityToken:    o.capabilityToken,
		desc:               desc,
		dek:                dek,
		passphraseProvider: o.passphrase,
	}
	return s, nil
}

// unlockBootstrap completes the bootstrap-into-Unlocked transition
// shared by InitStore (always), the Plain-DEK OpenStore path,
// the AutoUnlock OpenStore path, and the deferred Store.Unlock
// path (1.2b.5).
//
// The caller has produced a *store in StateBootstrapping with
// the DEK already populated. unlockBootstrap runs the Orphan
// Scan per §10.2, publishes the report, and flips state to
// StateUnlocked atomically.
//
// Errors from the Orphan Scan propagate; the *store is left in
// StateBootstrapping. The caller decides whether to retry, fall
// back to Locked, or surface the failure.
func unlockBootstrap(ctx context.Context, s *store, pub Publisher) error {
	report, err := recoverOrphans(ctx, s.drv, s.index)
	if err != nil {
		return fmt.Errorf("orphan scan: %w", err)
	}
	publishOrphanReport(pub, report)

	s.stateMu.Lock()
	s.state = domain.StateUnlocked
	s.stateMu.Unlock()
	return nil
}

// validateAgainstActiveConfig checks that the caller-supplied
// StoreConfig agrees with the active system.config on every
// immutable parameter. Mutable parameters are not compared — they
// are reassignable through UpdateConfig (M2+).
//
// Only fields the caller actually populated (non-zero values in the
// requested config) are compared; a caller who passes WithConfig{}
// or partial WithConfig with only mutable fields passes through.
// A caller who passes an immutable that does not match the active
// config gets errs.ErrConfigMismatch.
//
// Rationale for "non-zero comparison": go zero values are
// indistinguishable from "field omitted". The caller can always
// pass an explicit value to opt into the check; a default value
// passes silently. This matches the contract documented in
// 4. API Reference/01 Lifecycle §1.2.
func validateAgainstActiveConfig(req, active domain.StoreConfig) error {
	var mismatches []string

	if req.PathTopology != "" && req.PathTopology != active.PathTopology {
		mismatches = append(mismatches,
			fmt.Sprintf("PathTopology: requested %q, active %q",
				req.PathTopology, active.PathTopology))
	}
	if req.ManifestStorage != "" && req.ManifestStorage != active.ManifestStorage {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestStorage: requested %q, active %q",
				req.ManifestStorage, active.ManifestStorage))
	}
	if req.ManifestEncoding != "" && req.ManifestEncoding != active.ManifestEncoding {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestEncoding: requested %q, active %q",
				req.ManifestEncoding, active.ManifestEncoding))
	}
	if req.ManifestCrypto != "" && req.ManifestCrypto != active.ManifestCrypto {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestCrypto: requested %q, active %q",
				req.ManifestCrypto, active.ManifestCrypto))
	}
	if req.ContentHasher != "" && req.ContentHasher != active.ContentHasher {
		mismatches = append(mismatches,
			fmt.Sprintf("ContentHasher: requested %q, active %q",
				req.ContentHasher, active.ContentHasher))
	}
	// DeletionPolicyLock: bool, "not set" indistinguishable from
	// "false". Compare only when the caller explicitly asked to
	// lock — false is the relaxed default and passing it should not
	// fail against a locked active config.
	if req.DeletionPolicyLock && !active.DeletionPolicyLock {
		mismatches = append(mismatches,
			"DeletionPolicyLock: requested true, active false")
	}

	if len(mismatches) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errs.ErrConfigMismatch, strings.Join(mismatches, "; "))
}
