package assembly

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/customindex"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/extension"
	"scrinium.dev/internal/uri"
	"scrinium.dev/present"
	"scrinium.dev/projection"
)

type buildMode int

const (
	modeOpen buildMode = iota
	modeInit
	modeOpenOrInit
)

// agentWiring carries the build-time agent/scheduler options into the
// single-store assembler, so the signatures stay stable as options grow.
type agentWiring struct {
	handlers   []func(event.Event)
	stdSched   bool
	cronParser agent.CronParser
	// schedules (kind -> cron/interval expr) and agentConfigs (kind ->
	// config) are build-time overrides from WithSchedule/WithAgentConfig,
	// applied to the scheduler during assembly (§9.7). Last-wins per kind.
	schedules    map[string]string
	agentConfigs map[string]any
	// extensions are the per-build extensions from WithExtension, installed
	// alongside the process-wide registry set.
	extensions []extension.Extension
	// passphrase, if set via WithPassphrase, overrides the policy-derived
	// passphrase provider.
	passphrase domain.PassphraseProvider
}

// build turns a validated, defaulted Config into an assembled stack. It
// assembles the single-store path (the one the engine fully supports
// today); everything that depends on not-yet-wired components returns
// errs.ErrNotImplemented with a pointer to the milestone chunk that
// lands it.
func build(ctx context.Context, c *Config, mode buildMode, aw agentWiring) (Assembly, error) {
	if len(c.Stores) > 0 {
		return nil, fmt.Errorf("scrinium: multistore assembly is not wired yet (M4/S1): %w", errs.ErrNotImplemented)
	}
	if c.Store == nil {
		return nil, fmt.Errorf("scrinium: no store to build")
	}
	return buildSingle(ctx, c, mode, aw)
}

func buildSingle(ctx context.Context, c *Config, mode buildMode, aw agentWiring) (_ Assembly, retErr error) {
	spec := c.Store
	if err := guardUnsupportedPolicy(spec.Policy); err != nil {
		return nil, err
	}

	var cleanups []func()
	defer func() {
		if retErr == nil {
			return
		}
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()

	// 1. Driver.
	drv, err := dialDriver(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("scrinium: driver: %w", err)
	}

	// 2. For an Init/OpenOrInit on a local store, ensure the directory.
	if mode != modeOpen {
		if p, perr := uri.ResolveLocalURI(spec.Driver); perr == nil {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return nil, fmt.Errorf("scrinium: mkdir store: %w", err)
			}
		}
	}

	// 3. Index (default ladder: explicit spec.Index, then Config.Defaults,
	//    then a built-in sqlite next to a local store).
	idx, err := dialIndex(ctx, spec, c.Defaults)
	if err != nil {
		return nil, fmt.Errorf("scrinium: index: %w", err)
	}
	cleanups = append(cleanups, func() {
		if err := idx.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium: index close on rollback: %v\n", err)
		}
	})

	// 4. Extensions — installed as wholes; each part is applied at its
	//    construction phase (ADR-88, Principle 12 = same result as a
	//    one-call Use over a live target). The index axis must precede
	//    store open so the first IndexManifest dispatches into it; the
	//    behavior wrapper is applied at open; paired agents join the
	//    scheduler. The set comes from the registry (registered at the
	//    composition root, ADR-63/98); the assembler special-cases no
	//    extension — the projection view seams below are taken from
	//    whichever extensions provide those views.
	//
	// Two sources, installed identically below: the process-wide registry
	// (RegisterExtension — e.g. third-party blank-import) and the per-build
	// WithExtension options. A duplicate view Root across them is rejected
	// by the install loop, so double-installing one extension is caught.
	exts := append(reg.extensionList(), aw.extensions...)
	var (
		loadedExts    []extension.Descriptor
		wrapFactories []wrapper.Factory
		extAgents     []extension.Agent
	)
	// View contributions discovered across installed extensions (ADR-98);
	// the composition root unions them with the native views and feeds the
	// projection below. Root is unique per view.
	providedViews := map[string]customindex.ProvidedView{}
	presenters := present.Registry{}
	for _, e := range exts {
		loadedExts = append(loadedExts, e.Descriptor())
		if ci, ok := e.CustomIndex(); ok {
			if host, ok := idx.(customindex.Host); ok {
				if err := host.CustomIndexes().Register(ctx, ci); err != nil {
					return nil, fmt.Errorf("scrinium: extension %q custom index: %w", e.Descriptor().Name, err)
				}
			}
			if vp, ok := ci.(customindex.ViewProvider); ok {
				for _, pv := range vp.ProvidedViews() {
					if _, dup := providedViews[pv.Root]; dup {
						return nil, fmt.Errorf("scrinium: view %q provided by more than one extension", pv.Root)
					}
					providedViews[pv.Root] = pv
				}
			}
			if sp, ok := ci.(present.SchemaPresenter); ok {
				for _, sc := range sp.PresentedSchemas() {
					if _, dup := presenters[sc.Key]; dup {
						return nil, fmt.Errorf("scrinium: schema %q presented by more than one extension", sc.Key)
					}
					presenters[sc.Key] = sc
				}
			}
		}
		if f, ok := e.Wrapper(); ok {
			wrapFactories = append(wrapFactories, f)
		}
		extAgents = append(extAgents, e.Agents()...)
	}

	// 5. StoreConfig + passphrase from the policy.
	cfg, _ := storeConfigFromPolicy(spec.Policy)
	pp, err := passphraseProvider(ctx, spec.Policy)
	if err != nil {
		return nil, fmt.Errorf("scrinium: passphrase: %w", err)
	}
	if aw.passphrase != nil {
		pp = aw.passphrase // WithPassphrase: option takes precedence over policy
	}

	// Event bus: shared by the store and the agents the assembler wires,
	// so a host can subscribe to both through one channel. Build-time
	// handlers (WithEventHandler) are attached now, before anything
	// publishes, so they observe events emitted during assembly.
	bus := event.NewEventBus()
	for _, h := range aw.handlers {
		bus.Subscribe(h)
	}

	storeOpts := []store.StoreOption{
		store.WithStoreIndex(idx),
		store.WithHashRegistry(hashRegistry()),
		store.WithConfig(cfg),
		store.WithPublisher(bus),
	}
	if pp != nil {
		storeOpts = append(storeOpts, store.WithPassphrase(pp), store.WithAutoUnlock())
	}

	st, created, kit, err := openOrInitStore(ctx, drv, mode, storeOpts)
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, func() {
		if err := st.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium: store close on rollback: %v\n", err)
		}
	})
	if st.State() == domain.StateLocked {
		return nil, fmt.Errorf("scrinium: store is locked; check the encryption passphrase")
	}

	// Behavior axis (Tier 3): wrap the store's data plane with each
	// extension's decorator, innermost-first, then keep the full Store via
	// a composite — administrative/maintenance operations pass through to
	// the underlying store unchanged. The composite is what the client and
	// the projection read through.
	if len(wrapFactories) > 0 {
		// Rules Engine (ADR-75): validate the stack before composing —
		// structural order/closed-set, ≤1 of each structural, chunker not
		// on Backup, size invariant. Single-store open has no Backup; the
		// size inputs are threaded once chunker/bundler config is wired.
		descs := make([]wrapper.Descriptor, len(wrapFactories))
		for i, f := range wrapFactories {
			descs[i] = f.Descriptor()
		}
		if verr := wrapper.Validate(descs, wrapper.ValidateOptions{}); verr != nil {
			return nil, fmt.Errorf("scrinium: wrapper composition: %w", verr)
		}
		data := store.DataStore(st)
		for _, f := range wrapFactories {
			d, werr := f.Wrap(data, wrapper.Deps{Publisher: bus})
			if werr != nil {
				return nil, fmt.Errorf("scrinium: apply extension wrapper: %w", werr)
			}
			data = d
		}
		st = wrappedStore{DataStore: data, AdminStore: st}
	}

	// Late-bound extension Env (ADR-100/101 §4): now that the store is
	// open, hand each extension that needs it a scoped SystemStore — its
	// own "extension.<name>." slice of System(). The assembler scopes it
	// by the extension's own Descriptor name and the handle is confined
	// there, so a plugin can address only its own slice; the assembler
	// knows nothing of what any plugin keeps in it (durable registries,
	// cursors, …). Extensions with no durable state do not implement
	// Receiver and are skipped — the same assertion pattern as the index,
	// wrapper, and agent axes.
	for _, e := range exts {
		r, ok := e.(extension.Receiver)
		if !ok {
			continue
		}
		// Pre-write veto (ADR-108): if the extension validates its own scoped
		// system artifacts, install it on the scope the assembler hands back.
		// by-assertion — extensions without a veto are unaffected.
		var scopedOpts []extension.ScopedOption
		if val, ok := e.(extension.SystemArtifactValidator); ok {
			scopedOpts = append(scopedOpts, extension.WithValidator(val))
		}
		scoped, serr := extension.NewScopedSystemStore(e.Descriptor().Name, st.System(), scopedOpts...)
		if serr != nil {
			return nil, fmt.Errorf("scrinium: extension %q scoped system store: %w", e.Descriptor().Name, serr)
		}
		if uerr := r.UseEnv(extension.Env{SystemStore: scoped}); uerr != nil {
			return nil, fmt.Errorf("scrinium: extension %q env: %w", e.Descriptor().Name, uerr)
		}
	}

	// Agents. Validate configured kinds against the registry (fail fast,
	// like an unknown driver scheme) and assemble the deps the assembler
	// hands agents directly: Driver and StoreIndex never leave the
	// assembler (see Assembly.CustomIndexes doc). No scheduler and no
	// goroutine here — scheduling is opt-in (ADR-69 level 1 default).
	for _, ag := range c.Agents {
		if _, ok := agent.Lookup(ag.Kind); !ok {
			return nil, fmt.Errorf("scrinium: no agent registered for kind %q (blank-import the agent package, as with drivers)", ag.Kind)
		}
	}
	agentDeps := agent.AgentDeps{
		Publisher: bus,
		Driver:    drv,
		Index:     idx,
		HostID:    uuid.NewString(), // per-process actor id for lease/takeover events
		// StoreID is left empty until the store exposes its descriptor id;
		// it only tags agent events (diagnostics), not lease identity.
	}

	// Scheduler (ADR-69, §9.7). It is active if WithStandardScheduler was
	// passed, or any schedule was declared — config policy blocks
	// (gc/scrub/checkpoint), agents[] triggers, or the WithSchedule option.
	// Declared schedules are resolved (interval or cron) with replace-by-kind
	// layering, then registered. When active, gc/scrub/checkpoint hygiene
	// defaults join the set for registered kinds. A cron schedule without a
	// cron adapter (cron.Enable) is a fail-fast error.
	scheds, serr := resolveSchedules(spec, c, aw)
	if serr != nil {
		return nil, serr
	}

	// Paired agents from extensions (background add-on): the kind must be
	// registered; a declared schedule (interval or cron) joins the set
	// (replace-by-kind) and activates the scheduler, mirroring config
	// schedules. Without a schedule the kind is available via
	// RunMaintenance only.
	for _, ea := range extAgents {
		if _, ok := agent.Lookup(ea.Kind); !ok {
			return nil, fmt.Errorf("scrinium: extension agent %q not registered (blank-import its package, as with drivers)", ea.Kind)
		}
		if ea.Schedule == "" {
			continue
		}
		rs := resolvedSchedule{cfg: ea.Config}
		if d, derr := time.ParseDuration(ea.Schedule); derr == nil {
			rs.interval = d
		} else if aw.cronParser == nil {
			return nil, fmt.Errorf("scrinium: extension agent %q has cron schedule %q but cron is not enabled (cron.Enable())", ea.Kind, ea.Schedule)
		} else {
			next, cerr := aw.cronParser(ea.Schedule)
			if cerr != nil {
				return nil, fmt.Errorf("scrinium: extension agent %q schedule %q: %w", ea.Kind, ea.Schedule, cerr)
			}
			rs.next = next
		}
		scheds[ea.Kind] = rs
	}
	schedActive := aw.stdSched || len(scheds) > 0
	if schedActive {
		addHygieneDefaults(scheds, aw)
	}
	var sched agent.Scheduler
	var stopTicker func() = func() {}
	if schedActive {
		s, nerr := agent.NewScheduler(st, agentDeps)
		if nerr != nil {
			return nil, fmt.Errorf("scrinium: scheduler: %w", nerr)
		}
		sched = s
		for kind, rs := range scheds {
			if aerr := sched.Add(agent.Schedule{
				Agent:    kind,
				Interval: rs.interval,
				Next:     rs.next,
				Config:   rs.cfg,
			}); aerr != nil {
				return nil, fmt.Errorf("scrinium: schedule %q: %w", kind, aerr)
			}
		}
		done := make(chan struct{})
		var once sync.Once
		stopTicker = func() { once.Do(func() { close(done) }) }
		go func() {
			tk := time.NewTicker(standardSchedulerTick)
			defer tk.Stop()
			for {
				select {
				case <-done:
					return
				case now := <-tk.C:
					_ = sched.Tick(now)
				}
			}
		}()
		cleanups = append(cleanups, func() {
			stopTicker()
			_ = sched.Stop(context.Background())
		})
	}

	// 6. Read-side View + read/write FSOps from the projection section.
	effProj := c.Projection
	mountSession := domain.NewMountSessionID()

	// Bundle the read/write ends into one Projection. nil when the
	// config had no projection section — consumers (apps) that only
	// need the store never touch it.
	var proj *projection.Projection
	if effProj != nil {
		pcfg, cfgErr := projectionConfig(effProj, mountSession, spec.Driver)
		if cfgErr != nil {
			return nil, fmt.Errorf("scrinium: %w", cfgErr)
		}
		// Every view an extension backs is forwarded to the projection
		// verbatim (ADR-98): the assembler special-cases none and names no
		// view — it adapts each customindex.ProvidedView onto the
		// projection's own type and hands over the bulk metadata source a
		// view may carry (today only fspath's index does). Sorted by Root so
		// the projection sees a deterministic order regardless of the order
		// extensions were installed.
		var metaSrc projection.MetadataIndex
		roots := make([]string, 0, len(providedViews))
		for root := range providedViews {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		pcfg.ProvidedViews = make([]projection.ProvidedView, 0, len(roots))
		for _, root := range roots {
			pv := providedViews[root]
			pcfg.ProvidedViews = append(pcfg.ProvidedViews, projection.ProvidedView{
				Root:     pv.Root,
				Path:     pv.Path,
				Collide:  pv.Collide,
				Orphans:  pv.Orphans,
				CountKey: pv.CountKey,
			})
			if pv.Metadata != nil && metaSrc == nil {
				metaSrc = pv.Metadata
			}
		}
		// Synchronization seam (ADR-107): when the index backs the
		// SyncSource/SyncWaiter capability, hand it to the projection so the
		// view converges on other clients' writes. by-assertion — a
		// single-client index leaves the projection a snapshot. When the index
		// can also resolve manifests by digest, the delta-capable adapter lets
		// the view upsert just the changes instead of re-walking.
		if ss, ok := idx.(index.SyncSource); ok {
			if res, ok := idx.(index.ManifestResolver); ok {
				pcfg.SyncSource = syncDeltaSource{ss: ss, res: res}
			} else {
				pcfg.SyncSource = syncTokenSource{ss}
			}
		}
		if sw, ok := idx.(index.SyncWaiter); ok {
			pcfg.SyncWaiter = syncWaiter{sw}
		}
		p, buildErr := projection.Build(ctx, st, metaSrc, pcfg)
		if buildErr != nil {
			return nil, fmt.Errorf("scrinium: %w", buildErr)
		}
		proj = p
		cleanups = append(cleanups, func() {
			if err := proj.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "scrinium: projection close on rollback: %v\n", err)
			}
		})
	}

	// closeFn unwinds in LIFO order; idempotency is the assembly's job.
	closeFn := func() error {
		var firstErr error
		if sched != nil {
			stopTicker()
			if err := sched.Stop(context.Background()); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if proj != nil {
			if err := proj.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := st.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := idx.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}

	info := Info{StoreURI: spec.Driver, Created: created}
	// Backend names for diagnostics, via the optional namer capabilities.
	// Both drv and idx are the raw constructed instances here, before any
	// store-internal wrapping, so the assertions reach the concrete backends.
	if n, ok := drv.(driver.DriverNamer); ok {
		info.StoreDriver = n.DriverName()
	}
	if n, ok := idx.(index.DriverNamer); ok {
		info.IndexDriver = n.DriverName()
	}
	if effProj != nil {
		info.Editing = effProj.Editing
		info.ReadOnly = effProj.ReadOnly
	}

	return New(st, idx, proj, mountSession, info, kit, closeFn, agentDeps, bus, sched, aw.cronParser, loadedExts, presenters), nil
}

// agentCfg returns the WithAgentConfig override for kind, or nil.
func agentCfg(aw agentWiring, kind string) any {
	if aw.agentConfigs == nil {
		return nil
	}
	return aw.agentConfigs[kind]
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
