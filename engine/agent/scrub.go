package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/internal/lease"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// --- Scrub Agent ---

// ScrubConfig configures the Scrub Agent.
type ScrubConfig struct {
	// Enabled toggles background verification.
	Enabled bool

	// ScanInterval is the interval between verification cycles.
	ScanInterval time.Duration

	// MaxAge — blobs whose last_verified_at is older than
	// now() - MaxAge are eligible for verification.
	MaxAge time.Duration

	// MaxAgeNativeChecksum is an extended MaxAge for blobs on
	// media that report CapNativeChecksum. Silent bit rot is
	// impossible there, so the verification rate can be lowered.
	MaxAgeNativeChecksum time.Duration

	// BatchSize is the number of blobs in a single StoreIndex
	// fetch.
	BatchSize int

	// Force makes a pass verify every blob and manifest regardless of
	// last_verified_at (the staleness cutoff is ignored). Used for an
	// ad-hoc full re-scan after a suspected media fault. Background
	// passes leave it false.
	Force bool
}

// ScrubStats are the statistics of a single Scrub cycle.
type ScrubStats struct {
	ScannedBlobs  int64
	VerifiedBlobs int64
	FailedBlobs   int64
}

// ScrubAgent is the background blob-integrity verifier.
// Engine-managed: a single Scrub Agent is launched automatically
// Agent for every registered Target Store.
type ScrubAgent interface {
	Agent

	// RunOnce performs one full verification pass over every blob
	// whose last_verified_at is older than MaxAge and returns. Used
	// for ad hoc runs after media-corruption suspicions.
	RunOnce(ctx context.Context) (ScrubStats, error)
}

// NewScrubAgent creates a Scrub Agent. Constructed by the assembler
// (engine-internal) with the raw Driver and StoreIndex it needs
// alongside store.Store: the blob pass walks ListUnverifiedBlobs and
// the manifest pass walks ListUnverifiedManifests on the index, while
// the verification itself goes through the Store (VerifyBlobRef /
// VerifyManifest). hostID is the boot-unique process id owning the
// scrub lease; storeID tags emitted events and may be empty.
//
// The agent is per-store by design: it verifies and stamps only this
// store's blobs and manifests. Cross-store dedup does not change that —
// integrity is "this copy", and each store runs its own Scrub
// (2. Internals / Multistore: admin/maintenance ops are per-store).
func NewScrubAgent(
	st store.Store,
	drv driver.Driver,
	idx index.StoreIndex,
	bus event.Publisher,
	hostID string,
	storeID string,
	cfg ScrubConfig,
	opts ...AgentOption,
) (ScrubAgent, error) {
	if st == nil || drv == nil || idx == nil || bus == nil {
		return nil, fmt.Errorf("agent.NewScrubAgent: store, driver, index and bus are required")
	}
	if hostID == "" {
		return nil, fmt.Errorf("agent.NewScrubAgent: hostID is required for the scrub lease")
	}
	cfg = applyScrubDefaults(cfg)
	return &scrubAgent{
		baseState: baseState{log: resolveAgentLogger(opts)},
		store:     st, drv: drv, idx: idx, bus: bus,
		hostID: hostID, storeID: storeID, cfg: cfg,
	}, nil
}

const (
	scrubLeasePath           = "system.state/scrub/lease"
	defaultScrubScanInterval = 24 * time.Hour
	defaultScrubMaxAge       = 30 * 24 * time.Hour
	defaultScrubBatchSize    = 1000
	defaultScrubLeaseTTL     = 30 * time.Minute
)

func applyScrubDefaults(cfg ScrubConfig) ScrubConfig {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultScrubScanInterval
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = defaultScrubMaxAge
	}
	if cfg.MaxAgeNativeChecksum <= 0 {
		// Default: same cadence as MaxAge. An operator lowers the rate
		// for native-checksum media explicitly.
		cfg.MaxAgeNativeChecksum = cfg.MaxAge
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultScrubBatchSize
	}
	return cfg
}

// scrubAgent is the concrete ScrubAgent.
type scrubAgent struct {
	store   store.Store
	drv     driver.Driver
	idx     index.StoreIndex
	bus     event.Publisher
	hostID  string
	storeID string
	cfg     ScrubConfig

	baseState
}

var _ ScrubAgent = (*scrubAgent)(nil)

// scrubFactory builds the Scrub agent from the registry (ADR-51).
type scrubFactory struct{}

func (scrubFactory) Name() string { return "scrub" }

func (scrubFactory) Build(st store.Store, cfg any, deps AgentDeps) (Agent, error) {
	c, _ := cfg.(ScrubConfig) // zero value on mismatch -> defaults
	return NewScrubAgent(st, deps.Driver, deps.Index, deps.Publisher, deps.HostID, deps.StoreID, c, WithAgentLogger(deps.Logger))
}

func init() { Register(scrubFactory{}) }

// AgentType is the short registry/event identifier.
func (a *scrubAgent) AgentType() string { return "scrub" }

// Validate checks preconditions. Scrub needs no maintenance mode; the
// scrub lease is acquired inside Run, so the only precondition here is
// a live context.
func (a *scrubAgent) Validate(ctx context.Context) error { return ctx.Err() }

// Run performs one verification pass and returns its AgentResult. The
// agent is periodically invoked by the scheduler (ADR-69): an
// interrupted pass resumes from last_verified_at on the next call.
func (a *scrubAgent) Run(ctx context.Context) (*domain.AgentResult, error) {
	a.setState(StateRunning, nil)
	stats, err := a.RunOnce(ctx)
	res := &domain.AgentResult{
		AgentType:   "scrub",
		StoreID:     a.storeID,
		CompletedAt: time.Now(),
		Stats: map[string]int64{
			"scanned_blobs":  stats.ScannedBlobs,
			"verified_blobs": stats.VerifiedBlobs,
			"failed_blobs":   stats.FailedBlobs,
		},
	}
	if err != nil {
		a.setState(StateFaulted, err)
		if isCtxErr(err) {
			res.Partial = true
			a.bus.Publish(event.Event{Type: event.EventAgentCancelled})
			return res, err
		}
		a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
			AgentType: "scrub", StoreID: a.storeID, Err: err,
		}})
		return res, err
	}
	a.setState(StateIdle, nil)
	return res, nil
}

// RunOnce performs one verification pass under the scrub lease: a blob
// pass (ListUnverifiedBlobs → VerifyBlobRef → MarkVerified → cascade to
// consuming manifests) and a manifest pass (ListUnverifiedManifests,
// covering Inline artifacts the blob pass cannot reach). A blob that
// fails verification is recorded and the pass continues — one bad blob
// must not abort the scrub.
func (a *scrubAgent) RunOnce(ctx context.Context) (ScrubStats, error) {
	a.bus.Publish(event.Event{Type: event.EventAgentStarted, Payload: event.AgentStartedPayload{
		AgentType: "scrub", StoreID: a.storeID, StartedAt: time.Now(),
	}})

	l, prev, err := lease.Acquire(ctx, a.drv, lease.Config{
		Path:      scrubLeasePath,
		HostID:    a.hostID,
		AgentType: "scrub",
		TTL:       defaultScrubLeaseTTL,
	})
	if err != nil {
		return ScrubStats{}, fmt.Errorf("agent.Scrub.RunOnce: acquire lease: %w", err)
	}
	if prev != nil {
		a.bus.Publish(event.Event{Type: event.EventAgentStaleLease, Payload: event.LeaseTakeoverPayload{
			LeaseKey: scrubLeasePath, PreviousHolder: prev.HostID,
			ExpiredAt: prev.ExpiresAt, TakenBy: a.hostID,
		}})
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	hbErr := make(chan error, 1)
	go func() { hbErr <- l.Heartbeat(runCtx) }()
	defer func() {
		cancel()
		if err := l.Release(context.WithoutCancel(ctx)); err != nil {
			a.logger().Warn("lease release failed; lease will expire via TTL", "err", err)
		}
	}()

	var stats ScrubStats
	cutoff := a.cutoff(time.Now())

	// Phase A — blobs (the expensive plaintext check).
	//
	// Collect the work list first, then close the cursor before
	// verifying. VerifyBlobRef issues its own index queries
	// (ManifestsByBlobRef, loadManifest); running them while the
	// ListUnverifiedBlobs cursor is still open nests a query inside an
	// open result set on the same connection pool, which deadlocks or
	// hits a stale connection. Materialising the (bounded) ref list up
	// front keeps each index interaction independent.
	var blobRefs []string
	blobErr := a.idx.ListUnverifiedBlobs(runCtx, cutoff, func(blobRef string) error {
		if err := runCtx.Err(); err != nil {
			return err
		}
		blobRefs = append(blobRefs, blobRef)
		return nil
	})
	for _, blobRef := range blobRefs {
		if err := runCtx.Err(); err != nil {
			blobErr = err
			break
		}
		stats.ScannedBlobs++
		switch err := a.store.VerifyBlobRef(runCtx, blobRef); {
		case err == nil:
			stats.VerifiedBlobs++
			if err := a.idx.MarkVerified(runCtx, blobRef, time.Now()); err != nil {
				a.logger().Warn("scrub: failed to record blob verification", "blob_ref", blobRef, "err", err)
			}
			a.cascadeStampConsumers(runCtx, blobRef)
		case errors.Is(err, errs.ErrArtifactNotFound):
			// No consuming manifest (race vs Delete/GC, or orphan):
			// skip, not a corruption. GC owns orphan removal.
		case isCtxErr(err):
			blobErr = err
		default:
			// Corruption: VerifyBlobRef already published
			// EventScrubFailed. Count and continue.
			stats.FailedBlobs++
		}
	}

	// Phase B — manifests (cheap; covers Inline artifacts with no blob).
	// Same collect-then-act discipline as Phase A.
	var manIDs []domain.ArtifactID
	manErr := a.idx.ListUnverifiedManifests(runCtx, cutoff, func(m domain.Manifest) error {
		if err := runCtx.Err(); err != nil {
			return err
		}
		manIDs = append(manIDs, m.ArtifactID)
		return nil
	})
	for _, id := range manIDs {
		if err := runCtx.Err(); err != nil {
			manErr = err
			break
		}
		switch err := a.store.VerifyManifest(runCtx, id); {
		case err == nil:
			if err := a.idx.MarkManifestVerified(runCtx, id, time.Now()); err != nil {
				a.logger().Warn("scrub: failed to record manifest verification", "artifact_id", id, "err", err)
			}
		case errors.Is(err, errs.ErrArtifactNotFound):
			// raced with Delete — skip.
		case isCtxErr(err):
			manErr = err
		default:
			stats.FailedBlobs++ // manifest corruption counts as a failure too
		}
	}

	a.bus.Publish(event.Event{Type: event.EventAgentCycle, Payload: domain.AgentResult{
		AgentType: "scrub", StoreID: a.storeID, CompletedAt: time.Now(),
		Stats: map[string]int64{
			"scanned_blobs":  stats.ScannedBlobs,
			"verified_blobs": stats.VerifiedBlobs,
			"failed_blobs":   stats.FailedBlobs,
		},
	}})

	if err := firstNonCtxErr(blobErr, manErr); err != nil {
		return stats, fmt.Errorf("agent.Scrub.RunOnce: %w", err)
	}
	// Surface lease loss if the heartbeat aborted mid-pass.
	select {
	case herr := <-hbErr:
		if herr != nil && !isCtxErr(herr) {
			return stats, fmt.Errorf("agent.Scrub.RunOnce: lease lost: %w", herr)
		}
	default:
	}
	return stats, nil
}

// cutoff is the staleness boundary: blobs/manifests last verified before
// it are eligible. Force verifies everything (zero time = "before now"
// for all rows, NULL included). Native-checksum media uses the relaxed
// MaxAgeNativeChecksum.
func (a *scrubAgent) cutoff(now time.Time) time.Time {
	if a.cfg.Force {
		// Everything is older than "now", so every row is eligible.
		return now
	}
	maxAge := a.cfg.MaxAge
	if a.store.Capabilities().Has(driver.CapNativeChecksum) {
		maxAge = a.cfg.MaxAgeNativeChecksum
	}
	return now.Add(-maxAge)
}

// cascadeStampConsumers re-verifies (cheaply) and stamps every manifest
// that references blobRef, after its blob has been confirmed. A
// multi-blob (TOC) manifest is only stamped once all of its blobs are
// fresh — until then VerifyManifest succeeds but the manifest reappears
// in the next manifest pass, which is harmless (the cheap check repeats).
func (a *scrubAgent) cascadeStampConsumers(ctx context.Context, blobRef string) {
	// Collect consumer ids first, then verify/stamp — VerifyManifest
	// queries the index (loadManifest), which must not run while the
	// ManifestsByBlobRef cursor is open (see Phase A note).
	var ids []domain.ArtifactID
	if err := a.idx.ManifestsByBlobRef(ctx, blobRef, func(m domain.Manifest) error {
		ids = append(ids, m.ArtifactID)
		return nil
	}); err != nil {
		a.logger().Warn("scrub: failed to enumerate blob consumers", "blob_ref", blobRef, "err", err)
	}
	for _, id := range ids {
		if a.store.VerifyManifest(ctx, id) == nil {
			if err := a.idx.MarkManifestVerified(ctx, id, time.Now()); err != nil {
				a.logger().Warn("scrub: failed to record manifest verification (cascade)", "artifact_id", id, "err", err)
			}
		}
		// A corrupt consumer manifest already emitted EventScrubFailed
		// inside VerifyManifest; the cascade does not abort on it.
	}
}

// isCtxErr reports whether err is a context cancellation/deadline.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// firstNonCtxErr returns the first non-nil, non-context error.
func firstNonCtxErr(errs ...error) error {
	for _, e := range errs {
		if e != nil && !isCtxErr(e) {
			return e
		}
	}
	return nil
}
