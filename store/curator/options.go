package curator

import (
	"fmt"

	"scrinium.dev/agent"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/store/driver"
	"scrinium.dev/store/store"
	"scrinium.dev/store/wrapper/host"
	"scrinium.dev/store/wrapper/multistore"
)

// CuratorOption is an option for the Curator constructor (New).
type CuratorOption func(*curatorOptions)

type curatorOptions struct {
	// Populated by With* options and consumed by curator.New.
	stores        []registeredStore
	backups       []registeredBackup
	hostStorage   *hostStorageRegistration
	multistoreIdx multistore.MultistoreIndex
	routingFunc   multistore.RoutingFunc
	metaRouter    multistore.MetadataRouter
	eventBus      event.EventBus
	scrubCfg      *agent.ScrubConfig
	snapshotCfg   *agent.SnapshotConfig
}

type registeredStore struct {
	id       string
	store    store.Store
	cfg      multistore.StoreRegistrationConfig
	wrappers []multistore.WrapperFactory
}

type registeredBackup struct {
	targetID string
	store    store.Store
	cfg      multistore.BackupConfig
	wrappers []multistore.WrapperFactory
}

type hostStorageRegistration struct {
	drv driver.Driver
	cfg host.HostStorageConfig
}

// WithStore registers a Target Store with Curator. Decorators are
// applied "outside in": the first wrapper is closest to the
// client, the last is closest to the underlying store.
func WithStore(id string, store store.Store, cfg multistore.StoreRegistrationConfig, wrappers ...multistore.WrapperFactory) CuratorOption {
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
func WithBackup(targetID string, store store.Store, cfg multistore.BackupConfig, wrappers ...multistore.WrapperFactory) CuratorOption {
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
func WithHostStorage(localDrv driver.Driver, cfg host.HostStorageConfig) CuratorOption {
	return func(o *curatorOptions) {
		o.hostStorage = &hostStorageRegistration{drv: localDrv, cfg: cfg}
	}
}

// WithMultistoreIndex provides the global-index implementation.
// Usually not required with a single Target Store.
func WithMultistoreIndex(idx multistore.MultistoreIndex) CuratorOption {
	return func(o *curatorOptions) { o.multistoreIdx = idx }
}

// WithRoutingFunc provides the function that selects Target Stores
// at write time.
func WithRoutingFunc(fn multistore.RoutingFunc) CuratorOption {
	return func(o *curatorOptions) { o.routingFunc = fn }
}

// WithMetadataRouter provides the function that reconstructs
// RoutingHints from the manifest fields (Namespace, Ext, Usr)
// at deferred-Drain time.
func WithMetadataRouter(fn multistore.MetadataRouter) CuratorOption {
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
