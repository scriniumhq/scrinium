package rebuild

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/internal/checkpointfmt"
	"scrinium.dev/engine/agent/internal/lease"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// RebuildSource is the strategy for picking a source when
// rebuilding the index.
type RebuildSource string

const (
	// RebuildSourceAuto — use a checkpoint if available; otherwise
	// fall back to a full scan.
	RebuildSourceAuto RebuildSource = "Auto"

	// RebuildSourceCheckpoint — requires a valid checkpoint; returns
	// ErrNoCheckpoint when none is available.
	RebuildSourceCheckpoint RebuildSource = "Checkpoint"

	// RebuildSourceFullScan — ignores any checkpoints; always does a
	// full Location scan.
	RebuildSourceFullScan RebuildSource = "FullScan"
)

// RebuildConfig configures the RebuildIndexAgent.
type RebuildConfig struct {
	// Source is the strategy: Auto (default), Checkpoint, or
	// FullScan.
	Source RebuildSource

	// RecoveryKit holds the Recovery Kit content as bytes. Required
	// when the Store is in StateCorrupted because every descriptor
	// replica was lost (store.json missing or invalid). Otherwise
	// nil.
	RecoveryKit []byte

	// BatchSize is the number of manifests per IndexManifest
	// transaction. Default 1000. A larger value is faster but
	// holds the StoreIndex lease for longer.
	BatchSize int

	// Parallelism is the number of workers reading manifests in
	// parallel from the Location. Default 8. For S3 16–32 makes
	// sense, for LocalFS 4–8.
	Parallelism int

	// LeaseTTL is the hold time for system.state/maintenance/lease.
	// Default 30m. For very large Stores (millions of manifests)
	// it makes sense to extend it — losing the lease aborts the
	// operation.
	LeaseTTL time.Duration

	// RecoveryOverlap widens the tail re-scan when restoring from a
	// checkpoint: the scan re-reads manifests modified since
	// (checkpoint time − RecoveryOverlap) rather than exactly the
	// checkpoint instant. The overlap absorbs clock skew and manifests
	// written concurrently with the vacuum. IndexManifest is idempotent,
	// so re-reading a handful of already-present manifests is harmless.
	// Zero means no overlap (tail starts exactly at the checkpoint
	// instant); a small positive value (minutes) is recommended.
	RecoveryOverlap time.Duration

	// IgnoreStoreID, when true, skips the store-identity guard that rejects a
	// checkpoint recorded for a different Store before restoring it. Use only
	// to force recovery from a checkpoint whose identity is known-good despite
	// a mismatch (e.g. a deliberately imported checkpoint).
	IgnoreStoreID bool
}

// RebuildStats are the final statistics of the operation and a
// progress snapshot.
type RebuildStats struct {
	// Source is the path actually taken (relevant for Auto).
	Source RebuildSource

	// CheckpointUsed is the checkpoint ID when Source != FullScan; an
	// empty string when starting from scratch.
	CheckpointUsed string

	// ManifestsScanned — total manifests read from the Location.
	ManifestsScanned int64

	// ManifestsIndexed — added to the StoreIndex.
	ManifestsIndexed int64

	// ManifestsSkipped — already in the checkpoint, not re-read.
	ManifestsSkipped int64

	// BlobsRegistered — rows in the blobs table (regular + chunks).
	BlobsRegistered int64

	// PacksIndexed — pack volume TOCs read and parsed.
	PacksIndexed int64

	// PointerRecovered — was system.config/current restored?
	PointerRecovered bool

	// DescriptorRewrote — was store.json rewritten from the
	// Recovery Kit?
	DescriptorRewrote bool

	// Duration is the operation's elapsed time.
	Duration time.Duration
}

// RebuildIndexAgent rebuilds the StoreIndex from manifests. It
// supports a fast path through a recent checkpoint with read-in of
// new objects and a full fallback Location scan. It is also used to
// restore store.json (when lost) and the system.config/current
// pointer (when dangling).
type RebuildIndexAgent interface {
	agent.Agent

	// Stats returns a progress snapshot during execution (safe to
	// call from another goroutine). After Run, returns the final
	// statistics.
	Stats() RebuildStats
}

// NewRebuildIndexAgent creates a RebuildIndexAgent. Constructed by the
// assembler (Variant B) with the raw Driver and StoreIndex: the rebuild
// reads manifest files straight off the Location through the Driver
// (the index is exactly what is being rebuilt, so it cannot be the
// source) and writes the reconstructed rows through the StoreIndex.
// hostID owns the maintenance lease; storeID tags events.
//
// Scope on M3: the FullScan path reconstructs Blob manifests (Inline
// and Target) — the only manifest types that exist before M4 (Pack) and
// M5 (TOC/chunking). Encrypted manifests are decoded with the Store's own
// key material, obtained at run time (store.ManifestKeyProvider); an
// unencrypted Store has none and the scan stays Plain-only. The checkpoint
// fast-path (restoring a checkpoint produced by the checkpoint agent, then
// reading in the tail) is not yet wired into rebuild. Descriptor recovery
// from the Recovery Kit (rewriting a
// lost store.json) is implemented and runs before the scan when
// RecoveryKit is set; recovering a dangling system.config/current pointer
// is still out of scope (the kit carries no config). The remaining gaps
// are reported as explicit, non-silent boundaries rather than faked.
func NewRebuildIndexAgent(
	st store.Store,
	drv driver.Driver,
	idx index.StoreIndex,
	bus event.Publisher,
	hostID string,
	storeID string,
	cfg RebuildConfig,
	opts ...agent.AgentOption,
) (RebuildIndexAgent, error) {
	if st == nil || drv == nil || idx == nil || bus == nil {
		return nil, fmt.Errorf("rebuild.NewRebuildIndexAgent: store, driver, index and bus are required")
	}
	if hostID == "" {
		return nil, fmt.Errorf("rebuild.NewRebuildIndexAgent: hostID is required for the maintenance lease")
	}
	cfg = applyRebuildDefaults(cfg)
	return &rebuildAgent{
		BaseState: agent.NewBaseState(agent.ResolveLogger(opts...)),
		store:     st, drv: drv, idx: idx, bus: bus,
		hostID: hostID, storeID: storeID, cfg: cfg,
	}, nil
}

const (
	rebuildLeasePath        = "system.state/maintenance/lease"
	defaultRebuildBatchSize = 1000
	defaultRebuildParallel  = 8
	defaultRebuildLeaseTTL  = 30 * time.Minute
	manifestsPrefix         = "manifests"
)

func applyRebuildDefaults(cfg RebuildConfig) RebuildConfig {
	if cfg.Source == "" {
		cfg.Source = RebuildSourceAuto
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultRebuildBatchSize
	}
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = defaultRebuildParallel
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultRebuildLeaseTTL
	}
	return cfg
}

// rebuildAgent is the concrete RebuildIndexAgent.
type rebuildAgent struct {
	store   store.Store
	drv     driver.Driver
	idx     index.StoreIndex
	bus     event.Publisher
	hostID  string
	storeID string
	cfg     RebuildConfig

	mu    sync.Mutex
	stats RebuildStats

	agent.BaseState
}

var _ RebuildIndexAgent = (*rebuildAgent)(nil)

// Validate checks the operation is applicable without doing irreversible
// work. A Checkpoint-source request needs an index that can restore a
// checkpoint and at least one checkpoint to exist; otherwise it returns
// ErrNoCheckpoint. The maintenance-mode gate is enforced by the Store's
// RunMaintenance entry point (the sanctioned caller), not here — the
// current mode is not exposed on the read surface.
func (a *rebuildAgent) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.cfg.Source == RebuildSourceCheckpoint {
		if _, ok := a.idx.(index.CheckpointRestorer); !ok {
			return fmt.Errorf("rebuild.Rebuild.Validate: Source=Checkpoint: index cannot restore checkpoints: %w", errs.ErrNoCheckpoint)
		}
		_, _, ok, err := checkpointfmt.Latest(ctx, a.store.System())
		if err != nil {
			return fmt.Errorf("rebuild.Rebuild.Validate: Source=Checkpoint: %w", err)
		}
		if !ok {
			return fmt.Errorf("rebuild.Rebuild.Validate: Source=Checkpoint: %w", errs.ErrNoCheckpoint)
		}
	}
	return nil
}

// Run acquires the maintenance lease and rebuilds the index. Auto and
// Checkpoint take the checkpoint fast-path when a checkpoint exists and the
// index can restore one; Auto otherwise falls back to a full Location scan,
// and FullScan always scans.
func (a *rebuildAgent) run(ctx context.Context) (*domain.AgentResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	a.bus.Publish(event.Event{Type: event.EventAgentStarted, Payload: event.AgentStartedPayload{
		AgentType: "rebuild", StoreID: a.storeID, StartedAt: start,
	}})

	l, prev, err := lease.Acquire(ctx, a.drv, lease.Config{
		Path:      rebuildLeasePath,
		HostID:    a.hostID,
		AgentType: "rebuild",
		TTL:       a.cfg.LeaseTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("rebuild.Rebuild.Run: acquire lease: %w", err)
	}
	if prev != nil {
		a.bus.Publish(event.Event{Type: event.EventAgentStaleLease, Payload: event.LeaseTakeoverPayload{
			LeaseKey: rebuildLeasePath, PreviousHolder: prev.HostID,
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
			a.Logger().Warn("lease release failed; lease will expire via TTL", "err", err)
		}
	}()

	// Catastrophic recovery: rewrite store.json from the Recovery Kit
	// before the scan, under the maintenance lease, when every
	// descriptor replica was lost. The scan then repopulates the index
	// from the manifests that survived alongside the blobs.
	if a.cfg.RecoveryKit != nil {
		if err := a.restoreDescriptor(runCtx); err != nil {
			a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
				AgentType: "rebuild", StoreID: a.storeID, Err: err,
			}})
			return nil, fmt.Errorf("rebuild.Rebuild.Run: recovery kit restore: %w", err)
		}
	}

	// Key material for decoding encrypted manifests read straight off the
	// Location. nil for an unencrypted Store — the scan then handles Plain
	// manifests only (encrypted ones are skipped, as before).
	keys := store.ManifestKeyProvider(a.store)
	if err := a.rebuildIndex(runCtx, keys); err != nil {
		a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
			AgentType: "rebuild", StoreID: a.storeID, Err: err,
		}})
		return nil, fmt.Errorf("rebuild.Rebuild.Run: %w", err)
	}

	// Surface a lease loss that aborted the scan.
	select {
	case herr := <-hbErr:
		if herr != nil && !agent.IsCtxErr(herr) {
			return nil, fmt.Errorf("rebuild.Rebuild.Run: lease lost: %w", herr)
		}
	default:
	}

	a.mu.Lock()
	a.stats.Duration = time.Since(start)
	final := a.stats
	a.mu.Unlock()

	res := &domain.AgentResult{
		// AgentType matches the registered kind and the agent's other
		// events (started/failed/stale-lease) so consumers can correlate a
		// rebuild's events by a single type. ("RebuildIndex" remains the
		// lease owner tag, a separate concern.)
		AgentType:   "rebuild",
		StoreID:     a.storeID,
		CompletedAt: time.Now(),
		Stats: map[string]int64{
			"manifests_scanned": final.ManifestsScanned,
			"manifests_indexed": final.ManifestsIndexed,
			"blobs_registered":  final.BlobsRegistered,
		},
	}
	a.bus.Publish(event.Event{Type: event.EventAgentCompleted, Payload: *res})
	return res, nil
}

// restoreDescriptor rewrites store.json (and its L1 shadow) from the
// Recovery Kit in the config, for the catastrophic case where every
// on-disk descriptor replica was lost. It records the outcome in stats
// (DescriptorRewrote). The kit-to-descriptor mapping and the two-replica
// write live in the store package (RestoreDescriptorFromRecoveryKit),
// which owns the descriptor and kit formats; the agent only sequences it
// under the maintenance lease ahead of the scan.
func (a *rebuildAgent) restoreDescriptor(ctx context.Context) error {
	info, err := store.RestoreDescriptorFromRecoveryKit(ctx, a.drv, a.cfg.RecoveryKit)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.stats.DescriptorRewrote = info.DescriptorWritten
	a.mu.Unlock()
	return nil
}

// Stats returns a snapshot of progress (safe to call concurrently).
func (a *rebuildAgent) Stats() RebuildStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

// rebuildIndex selects the strategy. Auto and Checkpoint attempt the
// checkpoint fast-path first; Auto falls back to a full scan when no
// checkpoint is available, Checkpoint errors, and FullScan always scans.
func (a *rebuildAgent) rebuildIndex(ctx context.Context, keys artifact.KeyProvider) error {
	if a.cfg.Source != RebuildSourceFullScan {
		used, err := a.tryCheckpointFastPath(ctx, keys)
		if err != nil {
			return err
		}
		if used {
			return nil
		}
		if a.cfg.Source == RebuildSourceCheckpoint {
			return fmt.Errorf("rebuild: Source=Checkpoint but no checkpoint is available: %w", errs.ErrNoCheckpoint)
		}
		// Auto: fall through to a full scan.
	}
	a.setSource(RebuildSourceFullScan)
	return a.scanManifests(ctx, keys, time.Time{})
}

// tryCheckpointFastPath restores the newest checkpoint into the index and
// replays the tail of manifests written since. It returns used=false (nil
// error) when the index cannot restore checkpoints or none exists, leaving
// the caller to fall back. The checkpoint is fetched from the Store's own
// System() namespace, so it is by construction a checkpoint of this Store.
func (a *rebuildAgent) tryCheckpointFastPath(ctx context.Context, keys artifact.KeyProvider) (used bool, err error) {
	restorer, ok := a.idx.(index.CheckpointRestorer)
	if !ok {
		return false, nil
	}
	name, createdAt, ok, err := checkpointfmt.Latest(ctx, a.store.System())
	if err != nil {
		return false, fmt.Errorf("find latest checkpoint: %w", err)
	}
	if !ok {
		return false, nil
	}

	// RestoreCheckpoint needs an on-disk path; stream the artifact to a temp.
	tmpPath, cleanup, err := a.fetchCheckpoint(ctx, name)
	if err != nil {
		return false, fmt.Errorf("fetch checkpoint %q: %w", name, err)
	}
	defer cleanup()

	// Guard against restoring a checkpoint recorded for a different Store
	// (an import, a crossed mount). Skipped when IgnoreStoreID is set. The
	// check happens before the restore so a foreign checkpoint never touches
	// the index.
	if !a.cfg.IgnoreStoreID {
		if err := store.VerifyCheckpointIdentity(ctx, a.idx, tmpPath, a.storeID); err != nil {
			return false, fmt.Errorf("checkpoint %q: %w", name, err)
		}
	}

	if err := restorer.RestoreCheckpoint(ctx, tmpPath); err != nil {
		return false, fmt.Errorf("restore checkpoint %q: %w", name, err)
	}
	a.setSource(RebuildSourceCheckpoint)
	a.setCheckpointUsed(name)

	// Replay the tail: manifests modified since (checkpoint time − overlap).
	// IndexManifest is idempotent, so any overlap re-reads are harmless.
	since := createdAt.Add(-a.cfg.RecoveryOverlap)
	return true, a.scanManifests(ctx, keys, since)
}

// fetchCheckpoint streams the named checkpoint artifact from System() to a
// fresh temp file, returning its path and a cleanup that removes the temp
// directory. The caller must invoke cleanup.
func (a *rebuildAgent) fetchCheckpoint(ctx context.Context, name string) (path string, cleanup func(), err error) {
	noop := func() {}
	rh, err := a.store.System().Get(ctx, name)
	if err != nil {
		return "", noop, fmt.Errorf("get: %w", err)
	}
	defer rh.Close()

	tmpDir, err := os.MkdirTemp("", "scrinium-restore-")
	if err != nil {
		return "", noop, fmt.Errorf("temp dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }

	tmpPath := filepath.Join(tmpDir, "checkpoint.db")
	f, err := os.Create(tmpPath)
	if err != nil {
		cleanup()
		return "", noop, fmt.Errorf("create temp: %w", err)
	}
	if _, err := io.Copy(f, rh); err != nil {
		_ = f.Close()
		cleanup()
		return "", noop, fmt.Errorf("copy: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("close temp: %w", err)
	}
	return tmpPath, cleanup, nil
}

// scanManifests walks manifest files on the Location and reindexes them.
// since filters by modification time: the zero time scans everything (full
// rebuild); a non-zero time scans only the tail (checkpoint fast-path).
// Manifest paths are collected first (a streaming List whose callback only
// appends), then each file is fetched, decoded, and indexed — the per-file
// index writes must not run inside the List cursor.
func (a *rebuildAgent) scanManifests(ctx context.Context, keys artifact.KeyProvider, since time.Time) error {
	var paths []string
	listErr := a.drv.ListObjectsWithModTime(ctx, manifestsPrefix, since,
		func(om driver.ObjectMeta) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			paths = append(paths, om.Path)
			return nil
		})
	if listErr != nil && !agent.IsCtxErr(listErr) {
		return fmt.Errorf("list manifests: %w", listErr)
	}

	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.reindexManifestFile(ctx, p, keys); err != nil {
			if agent.IsCtxErr(err) {
				return err
			}
			// A single unreadable/unsupported manifest must not abort the
			// whole rebuild; it is recorded via a progress event and the
			// scan continues. Encrypted manifests land here only when no
			// KeyProvider is available (an unencrypted Store, or a Store
			// whose key material could not be resolved).
			a.bus.Publish(event.Event{Type: event.EventAgentProgress, Payload: event.AgentProgressPayload{
				AgentType: "rebuild", StoreID: a.storeID,
			}})
			continue
		}
	}
	return nil
}

// reindexManifestFile fetches one manifest file, decodes it, and writes
// the reconstructed index rows.
func (a *rebuildAgent) reindexManifestFile(ctx context.Context, path string, keys artifact.KeyProvider) error {
	digest, err := artifact.DigestFromManifestPath(path)
	if err != nil {
		return fmt.Errorf("parse manifest id from %q: %w", path, err)
	}
	rc, err := a.drv.Get(ctx, path)
	if err != nil {
		return fmt.Errorf("get manifest %q: %w", path, err)
	}
	data, err := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		a.Logger().Debug("rebuild: manifest reader close failed", "path", path, "err", cerr)
	}
	if err != nil {
		return fmt.Errorf("read manifest %q: %w", path, err)
	}

	var m domain.Manifest
	if keys != nil {
		// A KeyProvider is wired: DecodeEncrypted forwards Plain files and
		// decrypts encrypted ones, so both kinds are reconstructed rather
		// than the encrypted ones being skipped.
		m, err = artifact.DecodeEncrypted(data, keys)
	} else {
		// No key material: Plain only. An encrypted manifest returns
		// errs.ErrUnsupportedCrypto and is skipped by scanManifests.
		m, err = artifact.Decode(data)
	}
	if err != nil {
		return fmt.Errorf("decode manifest %q: %w", path, err)
	}
	a.countScanned()
	// The handle (m.ArtifactID) is serialised in the body and set by
	// Decode; the digest is the file name. A handle-less (system)
	// artifact falls back to its digest as ArtifactID.
	m.Digest = digest

	// Headless pack containers (empty slot) and chunked composites carry
	// chunk/packed-entry data absent from domain.Manifest on M3 — there is
	// nothing to reconstruct yet, so skip them rather than fake an index
	// row. Detect on the raw decoded manifest, before the handle backfill
	// below would mask an empty slot (ADR-83/92).
	if m.IsContainer() || m.IsComposite() {
		return nil
	}

	// User artifact: the handle (m.ArtifactID) is serialised in the body
	// and set by Decode. Fall back to the digest defensively if a
	// handle-bearing manifest somehow lacks one.
	if m.ArtifactID == "" {
		m.ArtifactID = domain.ArtifactID(digest)
	}
	return a.indexBlob(ctx, m)
}

// indexBlob reconstructs the IndexManifest arguments for a Blob manifest.
// Inline manifests carry their bytes in the body and have no blobs row;
// Target manifests resolve to a standalone blob file whose path is
// derived from the topology and the BlobRef.
func (a *rebuildAgent) indexBlob(ctx context.Context, m domain.Manifest) error {
	var addr domain.PhysicalAddress
	if m.LayoutHeader.BlobStorage == domain.LayoutTarget {
		topology := a.store.Config().PathTopology
		p, err := artifact.BlobPath(topology, domain.BlobTypeRegular, string(m.BlobRefs[0]))
		if err != nil {
			return fmt.Errorf("blob path for %q: %w", m.BlobRefs[0], err)
		}
		addr = domain.PhysicalAddress{Path: p}
	}
	// Blob manifests reference no chunks and no packed entries.
	if err := a.idx.IndexManifest(ctx, m, addr, nil); err != nil {
		return fmt.Errorf("index manifest %q: %w", m.ArtifactID, err)
	}
	a.countIndexed(m.LayoutHeader.BlobStorage == domain.LayoutTarget)
	return nil
}

func (a *rebuildAgent) setSource(s RebuildSource) {
	a.mu.Lock()
	a.stats.Source = s
	a.mu.Unlock()
}

func (a *rebuildAgent) setCheckpointUsed(name string) {
	a.mu.Lock()
	a.stats.CheckpointUsed = name
	a.mu.Unlock()
}

func (a *rebuildAgent) countScanned() {
	a.mu.Lock()
	a.stats.ManifestsScanned++
	a.mu.Unlock()
}

func (a *rebuildAgent) countIndexed(registeredBlob bool) {
	a.mu.Lock()
	a.stats.ManifestsIndexed++
	if registeredBlob {
		a.stats.BlobsRegistered++
	}
	a.mu.Unlock()
}

// AgentType is the short registry/event identifier.
func (a *rebuildAgent) AgentType() string { return "rebuild" }

// Run is the contract entry point: it tracks lifecycle State around the
// rebuild core (which owns lease handling and event emission).
func (a *rebuildAgent) Run(ctx context.Context) (*domain.AgentResult, error) {
	a.SetState(agent.StateRunning, nil)
	res, err := a.run(ctx)
	if err != nil {
		a.SetState(agent.StateFaulted, err)
		return res, err
	}
	a.SetState(agent.StateIdle, nil)
	return res, nil
}

// rebuildFactory builds the Rebuild agent from the registry (ADR-51).
type rebuildFactory struct{}

func (rebuildFactory) Name() string { return "rebuild" }

func (rebuildFactory) Build(st store.Store, cfg any, deps agent.AgentDeps) (agent.Agent, error) {
	c, _ := cfg.(RebuildConfig) // zero value on mismatch -> defaults
	return NewRebuildIndexAgent(st, deps.Driver, deps.Index, deps.Publisher, deps.HostID, deps.StoreID, c, agent.WithAgentLogger(deps.Logger))
}
