package core

import (
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
	return s, kit, nil
}

// OpenStore opens an existing Store at the Location served by drv.
//
// Behaviour (M1.4 subset):
//  1. Read store.json. Missing → errs.ErrStoreNotFound. Unreadable →
//     errs.ErrStoreCorrupted.
//  2. Validate the descriptor against any caller-supplied
//     WithConfig: immutable parameters must match. Mismatch →
//     errs.ErrConfigMismatch. When WithConfig is omitted, immutable
//     fields are accepted as-is — a legitimate scenario for
//     diagnostic tools and projection-only consumers.
//  3. Validate that a StoreIndex was provided via WithStoreIndex.
//     core never opens an index itself; the caller is responsible
//     for the dependency. Missing → error.
//  4. Reconstruct the active StoreConfig: immutable parameters
//     come from the descriptor, mutable ones from WithConfig
//     (overlay) or defaults. In M2 this step will load
//     system.config/current as a real artifact pointer.
//  5. Construct *store. The state machine is simplified for M1.4:
//     ManifestCrypto == Plain → StateUnlocked. Encrypted Stores
//     (StateLocked, optional auto-unlock) arrive with the crypto
//     pipeline in M2.
//
// What does NOT happen yet (planned milestones in parens):
//   - Three-level descriptor consensus L0/L1/L2 (M2.2).
//   - system.config/current as an artifact pointer (M2).
//   - location.lock acquisition / lease model (M3.1).
//   - StoreIndex schema cross-check against descriptor (M2).
//   - WithAutoUnlock and Unlock proper (M2).
func OpenStore(ctx context.Context, drv driver.Driver, opts ...StoreOption) (Store, error) {
	if drv == nil {
		return nil, errors.New("core.OpenStore: nil driver")
	}

	// --- Resolve options ---

	o := storeOptions{}
	for _, fn := range opts {
		fn(&o)
	}

	// --- Read the descriptor ---

	desc, err := descriptor.Read(ctx, drv)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, errs.ErrStoreNotFound
	case err != nil:
		// Any non-ENOENT error from the descriptor pipeline (parse
		// failure, validation, schema mismatch) means the file is
		// present but unreadable — Store is corrupted from the
		// caller's perspective. The original error is wrapped so
		// debugging can still see the cause.
		return nil, fmt.Errorf("%w: %v", errs.ErrStoreCorrupted, err)
	}

	// --- StoreIndex dependency ---
	//
	// Required up-front: readSystemConfig has no use for it but the
	// orphan scan and the open Store do, and refusing here gives a
	// clearer error than failing later on a nil index.

	idx := o.storeIndex
	if idx == nil {
		return nil, fmt.Errorf(
			"core.OpenStore: WithStoreIndex is required (see DI Example)")
	}
	if o.hashRegistry == nil {
		return nil, fmt.Errorf(
			"core.OpenStore: WithHashRegistry is required to read system.config")
	}

	// --- Load the active StoreConfig from system.config/current ---
	//
	// Per §10.1.4 the pointer is the source of truth for projection
	// parameters. Defaults are applied to the loaded config so legacy
	// fields stay populated even when the writer omitted them.

	active, err := readSystemConfig(ctx, drv, o.hashRegistry)
	if err != nil {
		return nil, fmt.Errorf("core.OpenStore: read system.config: %w", err)
	}
	active = applyConfigDefaults(active)
	if err := validateImmutableConfig(active); err != nil {
		return nil, fmt.Errorf("%w: system.config produced invalid config: %v",
			errs.ErrStoreCorrupted, err)
	}

	// --- Validate WithConfig against the active config (immutable) ---
	//
	// Only runs when the caller explicitly passed WithConfig. The
	// "open without config" path is legitimate — diagnostic tools,
	// projections, even Curator wiring with multiple Stores often
	// opens each store without re-asserting its config.

	if o.cfg != nil {
		if err := validateAgainstActiveConfig(*o.cfg, active); err != nil {
			return nil, err
		}
	}

	// --- Determine final state ---
	//
	// M1.4 supports Plain only. Encrypted Stores will produce
	// StateLocked here (and possibly auto-unlock when M2 lands
	// WithAutoUnlock plus the crypto pipeline). For now anything
	// non-Plain is rejected — better an explicit "not implemented"
	// than a silently broken Store.
	if active.ManifestCrypto != domain.ManifestCryptoPlain {
		return nil, fmt.Errorf(
			"core.OpenStore: encrypted Stores (ManifestCrypto=%q) are not supported in M1.4; "+
				"crypto pipeline lands in M2",
			active.ManifestCrypto)
	}

	// --- Construct *store ---
	//
	// M2.2 Plain path: descriptor carries plaintext DEK; we hand
	// it straight to buildStore. Encrypted-DEK Stores arrive in
	// 1.2b.4.4 (Reconcile + auto-unlock). Until then the
	// "encrypted" branch above refuses to open them.
	s, err := buildStore(ctx, o, drv, idx, active, desc, desc.DEK)
	if err != nil {
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

	// Bootstrap recovery: Orphan Scan per docs §10.2. On a freshly
	// initialised Store all three sweeps walk over absent prefixes
	// and return an empty report instantly. On open the report
	// reflects actual divergence between disk and index.
	report, err := recoverOrphans(ctx, drv, idx)
	if err != nil {
		return nil, fmt.Errorf("orphan scan: %w", err)
	}
	publishOrphanReport(o.publisher, report)

	s.stateMu.Lock()
	s.state = domain.StateUnlocked
	s.stateMu.Unlock()

	return s, nil
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
