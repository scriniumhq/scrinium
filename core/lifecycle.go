package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/domain"
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
// a divergence in immutable fields produces ErrConfigMismatch.
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
//     ErrStoreAlreadyExists.
//  2. With WithForceReinit, wipe the structural state — the
//     descriptor, and (in M1.3) the manifests/ directory.
//     Existing blobs/ are NOT removed unless WithPurgeOnReinit
//     is also set; this lets a user start a fresh Store on top
//     of orphan blobs and let GC reclaim them.
//  3. Generate a fresh StoreID. Apply config defaults. Validate
//     immutable parameters.
//  4. Validate that a StoreIndex was provided via WithStoreIndex.
//     core does NOT open the index itself — the caller wires the
//     concrete implementation (sqlite.NewStore, in-memory, etc.)
//     and passes it as a dependency. This keeps core free of any
//     import dependency on index/* packages (DAG: core ← index).
//  5. Write store.json atomically through the Driver.
//  6. Construct the *store object in StateUnlocked and return.
//
// Recovery Kit on M1.3: returned as nil because ManifestCrypto
// defaults to Plain; the Recovery Kit is a meaningful artefact
// only for encrypted Stores, which arrive in M2.
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
				ErrStoreAlreadyExists, existing.StoreID)
		}
		// Force reinit: clean up structural state. We stay
		// conservative — only the well-known files are touched.
		// blobs/ stay in place unless purge is also requested
		// (purge wiring lands in M3 alongside the GC; M1.3 just
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
				ErrStoreCorrupted, probeErr)
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

	// --- Generate identity, write descriptor ---

	storeID, err := generateStoreID()
	if err != nil {
		// idx came from the caller via WithStoreIndex — we do not
		// own its lifecycle and must not close it on our error path.
		return nil, nil, err
	}

	desc := &descriptor.Descriptor{
		StoreID:            storeID,
		FormatVersion:      descriptor.CurrentFormatVersion,
		PathTopology:       string(cfg.PathTopology),
		ManifestStorage:    string(cfg.ManifestStorage),
		ManifestEncoding:   string(cfg.ManifestEncoding),
		ManifestCrypto:     string(cfg.ManifestCrypto),
		ContentHasher:      string(cfg.ContentHasher),
		DeletionPolicyLock: cfg.DeletionPolicyLock,
	}
	if err := descriptor.Write(ctx, drv, desc); err != nil {
		// Same as above: do not close caller-owned idx on our
		// error paths.
		return nil, nil, fmt.Errorf("core.InitStore: write descriptor: %w", err)
	}

	// --- Construct *store ---

	s := &store{
		storeID:         storeID,
		drv:             drv,
		index:           idx,
		pub:             o.publisher,
		activeConfig:    cfg,
		state:           StateBootstrapping,
		hashes:          o.hashRegistry,
		transformers:    o.readRegistry,
		keyResolver:     o.keyResolver,
		capabilityToken: o.capabilityToken,
	}

	// Bootstrap recovery: Orphan Scan per docs §10.2. On a freshly
	// initialised Store all three sweeps walk over absent prefixes
	// and return an empty report instantly; the call is here for
	// symmetry with OpenStore and to handle WithForceReinit, where
	// blobs/ and manifests/ may legitimately survive across reinits.
	report, err := recoverOrphans(ctx, drv, idx)
	if err != nil {
		return nil, nil, fmt.Errorf("core.InitStore: orphan scan: %w", err)
	}
	publishOrphanReport(o.publisher, report)

	s.stateMu.Lock()
	s.state = StateUnlocked
	s.stateMu.Unlock()

	// Recovery Kit: nil for Plain manifests. M2 fills this in for
	// encrypted Stores.
	var recoveryKit []byte
	return s, recoveryKit, nil
}

// OpenStore opens an existing Store at the Location served by drv.
//
// Behaviour (M1.3 subset):
//  1. Read store.json. Missing → ErrStoreNotFound. Unreadable →
//     ErrStoreCorrupted.
//  2. Validate the descriptor against any caller-supplied
//     WithConfig: immutable parameters must match. Mismatch →
//     ErrConfigMismatch. When WithConfig is omitted, immutable
//     fields are accepted as-is — a legitimate scenario for
//     diagnostic tools and projection-only consumers.
//  3. Validate that a StoreIndex was provided via WithStoreIndex.
//     core never opens an index itself; the caller is responsible
//     for the dependency. Missing → error.
//  4. Reconstruct the active StoreConfig: immutable parameters
//     come from the descriptor, mutable ones from WithConfig
//     (overlay) or defaults. In M2 this step will load
//     system.config/current as a real artifact pointer.
//  5. Construct *store. The state machine is simplified for M1.3:
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
		return nil, ErrStoreNotFound
	case err != nil:
		// Any non-ENOENT error from the descriptor pipeline (parse
		// failure, validation, schema mismatch) means the file is
		// present but unreadable — Store is corrupted from the
		// caller's perspective. The original error is wrapped so
		// debugging can still see the cause.
		return nil, fmt.Errorf("%w: %v", ErrStoreCorrupted, err)
	}

	// --- Reconstruct the active StoreConfig ---
	//
	// Immutable parameters come from the descriptor (the source of
	// truth for identity-bound config). Mutable ones come from the
	// caller-supplied WithConfig as an overlay; absent that, we
	// fall back to applyConfigDefaults so behaviour is well-defined
	// even when a caller opens with no config at all.
	//
	// In M2 this step grows into "load system.config/current and
	// merge with WithConfig"; the M1.3 shape is forward-compatible
	// because mutable parameters keep coming from a layer above
	// the descriptor.

	active, err := buildActiveConfig(desc, o.cfg)
	if err != nil {
		return nil, err
	}

	// --- Validate WithConfig against the descriptor (immutable) ---
	//
	// Only runs when the caller explicitly passed WithConfig. The
	// "open without config" path is legitimate — diagnostic tools,
	// projections, even Curator wiring with multiple Stores often
	// opens each store without re-asserting its config.

	if o.cfg != nil {
		if err := validateAgainstDescriptor(*o.cfg, desc); err != nil {
			return nil, err
		}
	}

	// --- StoreIndex dependency ---

	idx := o.storeIndex
	if idx == nil {
		return nil, fmt.Errorf(
			"core.OpenStore: WithStoreIndex is required (see DI Example)")
	}

	// --- Determine final state ---
	//
	// M1.3 supports Plain only. Encrypted Stores will produce
	// StateLocked here (and possibly auto-unlock when M2 lands
	// WithAutoUnlock plus the crypto pipeline). For now anything
	// non-Plain is rejected — better an explicit "not implemented"
	// than a silently broken Store.
	if active.ManifestCrypto != domain.ManifestCryptoPlain {
		return nil, fmt.Errorf(
			"core.OpenStore: encrypted Stores (ManifestCrypto=%q) are not supported in M1.3; "+
				"crypto pipeline lands in M2",
			active.ManifestCrypto)
	}

	// --- Construct *store ---

	s := &store{
		storeID:         desc.StoreID,
		drv:             drv,
		index:           idx,
		pub:             o.publisher,
		activeConfig:    active,
		state:           StateBootstrapping,
		hashes:          o.hashRegistry,
		transformers:    o.readRegistry,
		keyResolver:     o.keyResolver,
		capabilityToken: o.capabilityToken,
	}

	// Bootstrap recovery: Orphan Scan per docs §10.2. Runs on every
	// transition into Unlocked; for Plain Stores that is here, for
	// encrypted Stores it will move into Unlock when M2 lands.
	report, err := recoverOrphans(ctx, drv, idx)
	if err != nil {
		return nil, fmt.Errorf("core.OpenStore: orphan scan: %w", err)
	}
	publishOrphanReport(o.publisher, report)

	s.stateMu.Lock()
	s.state = StateUnlocked
	s.stateMu.Unlock()

	return s, nil
}

// buildActiveConfig assembles the StoreConfig that the open Store
// will use. Immutable fields come from the descriptor; mutable
// ones come from the caller-supplied overlay (or defaults if
// omitted). The result is validated through validateImmutableConfig
// so a corrupted descriptor cannot hand a malformed config to the
// Store.
func buildActiveConfig(desc *descriptor.Descriptor, overlay *domain.StoreConfig) (domain.StoreConfig, error) {
	cfg := domain.StoreConfig{}
	if overlay != nil {
		cfg = *overlay
	}
	cfg = applyConfigDefaults(cfg)

	// Descriptor-bound immutables override anything the caller
	// passed. This is what makes the descriptor the source of
	// truth for identity-shaped config. WithConfig divergence is
	// caught separately by validateAgainstDescriptor — here we
	// just ensure the resulting StoreConfig matches what is
	// actually persisted.
	cfg.PathTopology = domain.PathTopology(desc.PathTopology)
	cfg.ManifestStorage = domain.ManifestStorage(desc.ManifestStorage)
	cfg.ManifestEncoding = domain.ManifestEncoding(desc.ManifestEncoding)
	cfg.ManifestCrypto = domain.ManifestCrypto(desc.ManifestCrypto)
	cfg.ContentHasher = domain.ContentHashAlgorithm(desc.ContentHasher)
	cfg.DeletionPolicyLock = desc.DeletionPolicyLock

	if err := validateImmutableConfig(cfg); err != nil {
		return domain.StoreConfig{}, fmt.Errorf("%w: descriptor produced invalid config: %v",
			ErrStoreCorrupted, err)
	}
	return cfg, nil
}

// validateAgainstDescriptor checks that the caller-supplied
// StoreConfig agrees with the descriptor on every immutable
// parameter the descriptor records. Mutable parameters are NOT
// compared — they are legitimately reassignable through
// UpdateConfig on a running Store.
//
// We compare only those fields that the caller actually populated
// (non-zero values in the requested config). A caller who passes
// WithConfig{} or partial WithConfig with only mutable fields
// passes through cleanly. A caller who passes an immutable that
// does NOT match the descriptor gets ErrConfigMismatch.
//
// Rationale for the "non-zero comparison": go zero values are
// indistinguishable from "field omitted". The caller can always
// pass an explicit value to opt into the check; a default value
// passes silently. This matches the contract documented in
// 4. API Reference/01 Lifecycle §1.2.
func validateAgainstDescriptor(req domain.StoreConfig, desc *descriptor.Descriptor) error {
	mismatches := []string{}

	if req.PathTopology != "" && string(req.PathTopology) != desc.PathTopology {
		mismatches = append(mismatches,
			fmt.Sprintf("PathTopology: requested %q, descriptor has %q",
				req.PathTopology, desc.PathTopology))
	}
	if req.ManifestStorage != "" && string(req.ManifestStorage) != desc.ManifestStorage {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestStorage: requested %q, descriptor has %q",
				req.ManifestStorage, desc.ManifestStorage))
	}
	if req.ManifestEncoding != "" && string(req.ManifestEncoding) != desc.ManifestEncoding {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestEncoding: requested %q, descriptor has %q",
				req.ManifestEncoding, desc.ManifestEncoding))
	}
	if req.ManifestCrypto != "" && string(req.ManifestCrypto) != desc.ManifestCrypto {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestCrypto: requested %q, descriptor has %q",
				req.ManifestCrypto, desc.ManifestCrypto))
	}
	if req.ContentHasher != "" && string(req.ContentHasher) != desc.ContentHasher {
		mismatches = append(mismatches,
			fmt.Sprintf("ContentHasher: requested %q, descriptor has %q",
				req.ContentHasher, desc.ContentHasher))
	}
	// DeletionPolicyLock: bool, "not set" indistinguishable from
	// "false" by zero-value rule. We compare ONLY when the caller
	// explicitly asked to lock — false is the relaxed default and
	// passing it to OpenStore should not fail against a locked
	// descriptor (the caller may simply not care). A locked
	// descriptor read into a Store stays locked regardless.
	if req.DeletionPolicyLock && !desc.DeletionPolicyLock {
		mismatches = append(mismatches,
			"DeletionPolicyLock: requested true, descriptor has false")
	}

	if len(mismatches) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrConfigMismatch, strings.Join(mismatches, "; "))
}
