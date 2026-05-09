package curator

import (
	"fmt"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/core"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
)

// CuratorOption is an option for the Curator constructor (New).
type CuratorOption func(*curatorOptions)

type curatorOptions struct {
	// Populated by With* options and consumed by curator.New.
	stores        []registeredStore
	backups       []registeredBackup
	hostStorage   *hostStorageRegistration
	multistoreIdx MultistoreIndex
	routingFunc   RoutingFunc
	metaRouter    MetadataRouter
	eventBus      event.EventBus
	scrubCfg      *agent.ScrubConfig
	snapshotCfg   *agent.SnapshotConfig
}

type registeredStore struct {
	id       string
	store    core.Store
	cfg      StoreRegistrationConfig
	wrappers []WrapperFactory
}

type registeredBackup struct {
	targetID string
	store    core.Store
	cfg      BackupConfig
	wrappers []WrapperFactory
}

type hostStorageRegistration struct {
	drv driver.Driver
	cfg HostStorageConfig
}

// WithStore registers a Target Store with Curator. Decorators are
// applied "outside in": the first wrapper is closest to the
// client, the last is closest to the underlying store.
func WithStore(id string, store core.Store, cfg StoreRegistrationConfig, wrappers ...WrapperFactory) CuratorOption {
	return func(o *curatorOptions) {
		o.stores = append(o.stores, registeredStore{
			id: id, store: store, cfg: cfg, wrappers: wrappers,
		})
	}
}

// WithBackup registers a Backup Store for a specific Target.
// Decorators are applied as in WithStore. Note: chunker.Wrapper
// on a Backup is forbidden by the Rules Engine (see
// docs/4. API Reference/05 Configuration §5.5).
func WithBackup(targetID string, store core.Store, cfg BackupConfig, wrappers ...WrapperFactory) CuratorOption {
	return func(o *curatorOptions) {
		o.backups = append(o.backups, registeredBackup{
			targetID: targetID, store: store, cfg: cfg, wrappers: wrappers,
		})
	}
}

// WithHostStorage registers the local-disk driver for the transit
// buffer. One per Curator. Without HostStorage the Local/Replicated/
// HostBuffered strategies and the bundler/chunker decorators are not
// available.
func WithHostStorage(localDrv driver.Driver, cfg HostStorageConfig) CuratorOption {
	return func(o *curatorOptions) {
		o.hostStorage = &hostStorageRegistration{drv: localDrv, cfg: cfg}
	}
}

// WithMultistoreIndex provides the global-index implementation.
// Usually not required with a single Target Store.
func WithMultistoreIndex(idx MultistoreIndex) CuratorOption {
	return func(o *curatorOptions) { o.multistoreIdx = idx }
}

// WithRoutingFunc provides the function that selects Target Stores
// at write time.
func WithRoutingFunc(fn RoutingFunc) CuratorOption {
	return func(o *curatorOptions) { o.routingFunc = fn }
}

// WithMetadataRouter provides the function that reconstructs
// RoutingHints from Manifest.Metadata at deferred-Drain time.
func WithMetadataRouter(fn MetadataRouter) CuratorOption {
	return func(o *curatorOptions) { o.metaRouter = fn }
}

// WithEventBus provides the event bus. Used by Curator itself for
// emitting curator.* events and forwarded to registered Stores
// through Publisher.
func WithEventBus(bus event.EventBus) CuratorOption {
	return func(o *curatorOptions) { o.eventBus = bus }
}

// WithScrubConfig configures the Curator-managed Scrub Agent.
// Curator automatically launches a Scrub for every registered
// Target Store.
func WithScrubConfig(cfg agent.ScrubConfig) CuratorOption {
	return func(o *curatorOptions) { o.scrubCfg = &cfg }
}

// WithSnapshotConfig configures the Curator-managed Snapshot
// Agent. Curator automatically launches a Snapshot for every
// registered Target Store with an available StoreIndex.
func WithSnapshotConfig(cfg agent.SnapshotConfig) CuratorOption {
	return func(o *curatorOptions) { o.snapshotCfg = &cfg }
}

// --- Configurations of Curator-managed agents ---
// These types are declared here (rather than in agent/) because
// Curator accepts them through its options. The agents themselves
// live in agent/scrub and agent/snapshot.

// New creates a Curator. It applies the options, validates the
// configuration (the Rules Engine for forbidden combinations),
// and starts the background services (Scrub, Snapshot) for the
// registered Targets.
//
// Implementation lands in M4.
func New(opts ...CuratorOption) (Curator, error) {
	return nil, fmt.Errorf("%w: curator.New", errs.ErrNotImplemented)
}
