package scrinium

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/driver"
	"github.com/rkurbatov/scrinium/engine/index"
	"github.com/rkurbatov/scrinium/engine/projection"
	"github.com/rkurbatov/scrinium/engine/projection/fsindex"
	"github.com/rkurbatov/scrinium/engine/projection/fsmeta"
)

// Init creates a fresh Scrinium store at the location described
// by cfg.Store, then opens it and returns a ready-to-use
// *Scrinium runtime. The store directory is created if it does
// not exist.
//
// The flow:
//
//  1. Resolve and create the store directory (file:// only).
//  2. Open driver via DialDriver.
//  3. Resolve and open index (sqlite by default for file://
//     stores).
//  4. Register fsindex extension.
//  5. core.InitStore — writes the descriptor + system.config.
//     If cfg.PassphraseFile is set, the store is created
//     encrypted and a recovery kit is produced.
//  6. Build View + FSOps to deliver a ready-to-use runtime.
//
// The recoveryKit return value is non-nil only for encrypted
// stores. The host MUST persist it (out of band — print, save
// to a vault, etc.) before the first Close. It is the only
// path back into the store if the passphrase is lost.
//
// On error, any partially-opened resources are cleaned up.
// Init does not delete the store directory on error: a half-
// initialised tree is recoverable; a deleted one is not.
func Init(ctx context.Context, cfg Config) (_ *Scrinium, recoveryKit []byte, retErr error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}

	// Deferred rollback for resources we open along the way.
	var cleanups []func()
	defer func() {
		if retErr == nil {
			return
		}
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()

	// 1. Ensure the store directory exists. Only file:// and
	//    bare paths are supported here — non-local stores are
	//    initialised by the host using their own tooling.
	storePath, err := localStorePath(cfg.Store)
	if err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: %w", err)
	}
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: mkdir store: %w", err)
	}

	// 2. Open driver.
	drv, err := driver.DialDriver(cfg.Store)
	if err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: %w", err)
	}

	// 3. Open index.
	indexURI, err := resolveIndexURI(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: %w", err)
	}
	idx, err := index.DialIndex(ctx, indexURI)
	if err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: open index: %w", err)
	}
	cleanups = append(cleanups, func() {
		if err := idx.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium.Init: index close on rollback: %v\n", err)
		}
	})

	// 4. Register fsindex.
	fsidx := fsindex.New()
	if extIdx, ok := idx.(indexWithExtensions); ok {
		if err := extIdx.Extensions().Register(ctx, fsidx); err != nil {
			return nil, nil, fmt.Errorf("scrinium.Init: register fsindex: %w", err)
		}
	}

	// 5. core.InitStore. The presence of cfg.PassphraseFile
	//    selects the encrypted-Init path (a passphrase
	//    provider is wired in, InitStore wraps the freshly
	//    generated DEK with a KEK derived from the passphrase
	//    and emits a recovery kit).
	pp, err := loadPassphraseProvider(cfg.PassphraseFile)
	if err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: %w", err)
	}
	storeOpts := []core.StoreOption{
		core.WithStoreIndex(idx),
		core.WithHashRegistry(defaultHashRegistry()),
	}
	if pp != nil {
		storeOpts = append(storeOpts, core.WithPassphrase(pp))
	}
	store, kit, err := core.InitStore(ctx, drv, storeOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: %w", err)
	}
	cleanups = append(cleanups, func() {
		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium.Init: store close on rollback: %v\n", err)
		}
	})

	// 6. Build View — the freshly-init'd store has no
	//    manifests yet, so backfill is essentially a no-op.
	//    We still set it up so the returned *Scrinium is
	//    immediately usable for Put/Get without a separate
	//    Open call.
	viewOpts := []projection.ViewOption{
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithFSIndex(fsidx),
	}
	if cfg.RootView != "" {
		viewOpts = append(viewOpts, projection.WithRootView(cfg.RootView))
	}
	if cfg.ByPathFallback != "" {
		viewOpts = append(viewOpts, projection.WithFallback(projection.PathFallback(cfg.ByPathFallback)))
	}
	view, err := projection.NewView(ctx, store, viewOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: build view: %w", err)
	}
	cleanups = append(cleanups, func() {
		if err := view.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium.Init: view close on rollback: %v\n", err)
		}
	})

	// 7. Mount session.
	mountSession := "mount-" + uuid.New().String()

	// 8. Scratch directory.
	//
	// See scrinium.Open for the rationale: we resolve the path
	// but do not create it. projection.FSOps lazy-creates it
	// at first write, and read-only setups skip it entirely.
	// Init does not need to clear leftover scratch — there is
	// no previous run to leave any.
	var scratchDir string
	if !cfg.ReadOnly {
		scratchDir, err = resolveScratchDir(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("scrinium.Init: %w", err)
		}
	}

	// 9. FSOps.
	fsopsOpts := []projection.FSOpsOption{
		projection.WithStore(store),
		projection.WithScratchQuota(cfg.ScratchQuota),
		projection.WithDefaultMode(cfg.DefaultMode),
		projection.WithDefaultUID(cfg.DefaultUID),
		projection.WithDefaultGID(cfg.DefaultGID),
		projection.WithEditingPolicy(cfg.editingPolicy()),
		projection.WithMountSession(mountSession),
		projection.WithNamespace(cfg.Namespace),
	}
	if scratchDir != "" {
		fsopsOpts = append(fsopsOpts, projection.WithScratchDir(scratchDir))
	}
	if cfg.ReadOnly {
		fsopsOpts = append(fsopsOpts, projection.WithReadOnly())
	}
	fsops, err := projection.NewFSOps(view, fsopsOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("scrinium.Init: build fsops: %w", err)
	}

	return &Scrinium{
		Config:       cfg,
		Store:        store,
		Index:        idx,
		View:         view,
		FSOps:        fsops,
		FSIndex:      fsidx,
		MountSession: mountSession,
	}, kit, nil
}
