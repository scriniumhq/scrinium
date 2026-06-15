package assembly

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"scrinium.dev/engine/extension/customindex"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/index/fsindex"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/projection"
)

type buildMode int

const (
	modeOpen buildMode = iota
	modeInit
	modeOpenOrInit
)

// standardSchedulerTick is how often the built-in scheduler goroutine
// (WithStandardScheduler) ticks the interval primitive. It bounds the
// latency between an agent becoming due and running, not the configured
// intervals themselves; 1s is ample for maintenance cadences.
const standardSchedulerTick = time.Second

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
		if p, perr := localStorePath(spec.Driver); perr == nil {
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

	// 4. fsindex extension — must precede store open so the first
	//    IndexManifest dispatches into it (single-store assembly path).
	fsidx := fsindex.New()
	if extIdx, ok := idx.(customindex.ExtensionHost); ok {
		if err := extIdx.Extensions().Register(ctx, fsidx); err != nil {
			return nil, fmt.Errorf("scrinium: register fsindex: %w", err)
		}
	}

	// 5. StoreConfig + passphrase from the policy.
	cfg, _ := storeConfigFromPolicy(spec.Policy)
	pp, err := passphraseProvider(ctx, spec.Policy)
	if err != nil {
		return nil, fmt.Errorf("scrinium: passphrase: %w", err)
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

	// Agents. Validate configured kinds against the registry (fail fast,
	// like an unknown driver scheme) and assemble the deps the assembler
	// hands agents directly: Driver and StoreIndex never leave the
	// assembler (see Assembly.Extensions doc). No scheduler and no
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
		p, buildErr := projection.Build(ctx, st, fsidx, pcfg)
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
	if effProj != nil {
		info.Namespace = effProj.Namespace
		info.Editing = effProj.Editing
		info.ReadOnly = effProj.ReadOnly
	}

	return New(st, idx, proj, mountSession, info, kit, closeFn, agentDeps, bus, sched, aw.cronParser), nil
}

// resolvedSchedule is a fully-resolved schedule for one kind: either an
// interval or a parsed cron Next, plus the config to build the agent with.
type resolvedSchedule struct {
	interval time.Duration
	next     func(time.Time) time.Time
	cfg      any
}

// resolveSchedules collects declared schedules into a kind->resolved map,
// replace-by-kind (later sources override earlier): config policy blocks,
// agents[] triggers, then the WithSchedule option. A cron expression
// requires the cron adapter (cron.Enable); without it, or on a parse error,
// resolveSchedules fails fast (§9.7).
func resolveSchedules(spec *StoreSpec, c *Config, aw agentWiring) (map[string]resolvedSchedule, error) {
	out := make(map[string]resolvedSchedule)
	set := func(kind string, every Duration, cron string, cfg any) error {
		rs := resolvedSchedule{cfg: cfg}
		switch {
		case cron != "":
			if aw.cronParser == nil {
				return fmt.Errorf("scrinium: agent %q declares a cron schedule %q but cron is not enabled (pass cron.Enable())", kind, cron)
			}
			next, err := aw.cronParser(cron)
			if err != nil {
				return fmt.Errorf("scrinium: agent %q cron schedule %q: %w", kind, cron, err)
			}
			rs.next = next
		case every > 0:
			rs.interval = time.Duration(every)
		default:
			return nil // no trigger declared
		}
		out[kind] = rs // replace-by-kind
		return nil
	}

	// Config policy blocks (gc/scrub/checkpoint). applyDefaults has filled the
	// cadence of a present block, so each present block carries a trigger.
	if spec != nil && spec.Policy != nil {
		p := spec.Policy
		if p.GC != nil {
			if err := set("gc", p.GC.Every, p.GC.Schedule, agentCfg(aw, "gc")); err != nil {
				return nil, err
			}
		}
		if p.Scrub != nil {
			if err := set("scrub", p.Scrub.Every, p.Scrub.Schedule, agentCfg(aw, "scrub")); err != nil {
				return nil, err
			}
		}
		if p.Checkpoint != nil {
			if err := set("checkpoint", p.Checkpoint.Every, p.Checkpoint.Schedule, agentCfg(aw, "checkpoint")); err != nil {
				return nil, err
			}
		}
	}

	// Config agents[] triggers. A WithAgentConfig override wins over the
	// inline config map.
	for _, ag := range c.Agents {
		if ag.Every == 0 && ag.Schedule == "" {
			continue
		}
		cfg := agentCfg(aw, ag.Kind)
		if cfg == nil && len(ag.Config) > 0 {
			cfg = ag.Config
		}
		if err := set(ag.Kind, ag.Every, ag.Schedule, cfg); err != nil {
			return nil, err
		}
	}

	// WithSchedule options override. expr is an interval (time.ParseDuration)
	// or, failing that, a cron expression.
	for kind, expr := range aw.schedules {
		if d, derr := time.ParseDuration(expr); derr == nil {
			if err := set(kind, Duration(d), "", agentCfg(aw, kind)); err != nil {
				return nil, err
			}
		} else if err := set(kind, 0, expr, agentCfg(aw, kind)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addHygieneDefaults adds the built-in maintenance schedules (gc/scrub/
// checkpoint) to an active scheduler's set, but only for kinds that are
// registered and not already scheduled. Technical hygiene runs whenever the
// scheduler is active (§10.2.9); a built-in that was not blank-imported (so
// not registered) is skipped rather than failing the build.
func addHygieneDefaults(out map[string]resolvedSchedule, aw agentWiring) {
	defaults := []struct {
		kind  string
		every Duration
	}{
		{"gc", defaultGCEvery},
		{"scrub", defaultScrubEvery},
		{"checkpoint", defaultCheckpointEvery},
	}
	for _, d := range defaults {
		if _, ok := out[d.kind]; ok {
			continue
		}
		if _, registered := agent.Lookup(d.kind); !registered {
			continue
		}
		out[d.kind] = resolvedSchedule{interval: time.Duration(d.every), cfg: agentCfg(aw, d.kind)}
	}
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
// RecoveryKit), no longer refusing encrypted initialisation.
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

// dialDriver resolves the store's driver: an extension factory if one
// is registered for the scheme, otherwise the engine's built-in
// DialDriver (file://, s3:// when present, bare paths). The built-in
// schemes are registered by the consumer's blank import (ADR-63).
func dialDriver(ctx context.Context, spec *StoreSpec) (driver.Driver, error) {
	scheme := schemeOf(spec.Driver)
	if f, ok := reg.driver(scheme); ok {
		creds, err := resolveCredentials(ctx, spec.Credentials)
		if err != nil {
			return nil, err
		}
		return f(ctx, spec.Driver, creds)
	}
	return driver.DialDriver(spec.Driver)
}

// dialIndex resolves the index along the default ladder (ADR-63): an
// explicit spec.Index wins; else Config.Defaults.Index; else a built-in
// sqlite under a local store dir. The resolved URI is dialled through an
// extension factory if one is registered for its scheme, otherwise the
// engine's built-in DialIndex.
func dialIndex(ctx context.Context, spec *StoreSpec, defaults *Defaults) (index.StoreIndex, error) {
	uri := spec.Index
	if uri == "" && defaults != nil {
		uri = defaults.Index
	}
	if uri == "" {
		p, err := localStorePath(spec.Driver)
		if err != nil {
			return nil, fmt.Errorf("index URI is empty and cannot default for store %q (set index explicitly)", spec.Driver)
		}
		uri = "sqlite:///" + filepath.Join(p, "index.db")
	}
	if f, ok := reg.indexFor(schemeOf(uri)); ok {
		creds, err := resolveCredentials(ctx, spec.Credentials)
		if err != nil {
			return nil, err
		}
		return f(ctx, uri, creds)
	}
	return index.DialIndex(ctx, uri)
}

func resolveCredentials(ctx context.Context, creds Credentials) (map[string][]byte, error) {
	if len(creds) == 0 {
		return nil, nil
	}
	out := make(map[string][]byte, len(creds))
	for name, ref := range creds {
		b, err := ref.Resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %w", name, err)
		}
		out[name] = b
	}
	return out, nil
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
	if p.DefaultPutNamespace != "" {
		cfg.DefaultPutNamespace = p.DefaultPutNamespace
	}

	return cfg, encrypted
}

// passphraseProvider builds a store.PassphraseProvider from the
// policy's encryption secret. The secret is resolved once at load
// time; the provider returns the same bytes on every prompt (init,
// unlock, rotation) — adequate for the file/env/plain schemes.
func passphraseProvider(ctx context.Context, p *Policy) (store.PassphraseProvider, error) {
	if p == nil || p.Encryption == nil {
		return nil, nil
	}
	secret, err := p.Encryption.Passphrase.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return func(_ context.Context, _ store.PassphraseHint) ([]byte, error) {
		// Hand back a copy: the engine zeroes the buffer after KEK
		// derivation, and we must survive a second prompt.
		cp := make([]byte, len(secret))
		copy(cp, secret)
		return cp, nil
	}, nil
}

func hashRegistry() domain.HashRegistry {
	return hashing.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

// --- URI helpers (a trimmed local copy of scrinium's; the assembler
// cannot import the root package without a layering inversion). ---

func schemeOf(uri string) string {
	if i := strings.Index(uri, "://"); i > 0 {
		return uri[:i]
	}
	if i := strings.IndexByte(uri, ':'); i > 0 && !strings.Contains(uri[:i], "/") {
		return uri[:i]
	}
	return ""
}

func localStorePath(storeURI string) (string, error) {
	if !looksLikeSchemeURI(storeURI) {
		return filepath.Abs(expandTilde(storeURI))
	}
	u, err := url.Parse(storeURI)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("non-local store scheme %q", u.Scheme)
	}
	switch u.Host {
	case "":
		return filepath.Abs(u.Path)
	case "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Abs(filepath.Join(home, strings.TrimPrefix(u.Path, "/")))
	case ".":
		return filepath.Abs("." + u.Path)
	}
	return "", fmt.Errorf("unsupported file:// host %q", u.Host)
}

func looksLikeSchemeURI(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := s[j]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '+', c == '-', c == '.':
			continue
		default:
			return false
		}
	}
	return true
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
