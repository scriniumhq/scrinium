package multistore

import (
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper/host"
)

// --- Policy enums ---

// WriteStrategy is the strategy for writing into a Target Store
// through Curator.
type WriteStrategy string

const (
	// WriteStrategyAuto — the engine decides based on the target
	// Store's capabilities. CapSlowRead becomes HostBuffered;
	// otherwise DirectStream.
	WriteStrategyAuto WriteStrategy = "Auto"

	// WriteStrategyHostBuffered — write into HostStorage with an
	// asynchronous Drain. The artifact is visible through Get
	// immediately, before the Drain completes.
	WriteStrategyHostBuffered WriteStrategy = "HostBuffered"

	// WriteStrategyDirectStream — write directly through the target
	// Store's Driver.
	WriteStrategyDirectStream WriteStrategy = "DirectStream"
)

// ReadCost labels the cost of a read. Used to keep cold Stores out
// of the regular Get flow.
type ReadCost string

const (
	ReadCostLow  ReadCost = "Low"
	ReadCostHigh ReadCost = "High"
)

// ReadPolicy controls when a Backup is read.
type ReadPolicy string

const (
	// ReadPolicyFallback — High Availability. Read automatically
	// when the Target is unavailable.
	ReadPolicyFallback ReadPolicy = "Fallback"

	// ReadPolicyNever — Compliance & Isolation. Fully excluded from
	// normal routing; reachable only through an explicit
	// Curator.Store(backupID).Get.
	ReadPolicyNever ReadPolicy = "Never"

	// ReadPolicyAuto — Storage Tiering. Excluded from regular Get,
	// but used when GetOptions.AllowColdRead is true.
	ReadPolicyAuto ReadPolicy = "Auto"
)

// AfterBackup controls what happens to the original in the Target
// after a successful backup.
type AfterBackup string

const (
	AfterBackupKeep   AfterBackup = "Keep"
	AfterBackupDelete AfterBackup = "Delete"
)

// OnUnavailable controls behaviour when a Backup is unavailable on
// the write path.
type OnUnavailable string

const (
	OnUnavailableBestEffort OnUnavailable = "BestEffort"
	OnUnavailableRequired   OnUnavailable = "Required"
	OnUnavailableQueued     OnUnavailable = "Queued"
)

// --- Configurations ---

// StoreRegistrationConfig are the parameters of registering a
// Target Store with Curator via WithStore.
type StoreRegistrationConfig struct {
	Priority             int
	ReadCost             ReadCost
	WriteStrategy        WriteStrategy
	AllowCrossStoreDedup bool
}

// BackupConfig are the parameters of registering a Backup Store
// via WithBackup.
type BackupConfig struct {
	PhysicalCopy  bool
	ReadPolicy    ReadPolicy
	AfterBackup   AfterBackup
	OnUnavailable OnUnavailable
	Priority      int
}

// --- Routing ---

// StoreTarget is one outcome of a RoutingFunc: where to write and
// at what priority.
type StoreTarget struct {
	StoreID  string
	Priority int
}

// RoutingMetadata is the input to RoutingFunc on the write path.
// Curator builds it from PutOptions.
type RoutingMetadata struct {
	Namespace   string
	Size        int64
	ContentType string
	Source      string
	Attributes  map[string]string
}

// RoutingFunc selects Target Stores for a write through Curator.
// It can be compiled from declarative configuration rules or
// supplied directly by the developer.
type RoutingFunc func(meta RoutingMetadata) []StoreTarget

// MetadataRouter reconstructs RoutingHints from the manifest
// (Namespace, Ext, Usr). Used at deferred-Drain time (DL-01)
// when the original hints from PutOptions are no longer
// available.
type MetadataRouter func(m domain.Manifest) domain.RoutingHints

// --- Decorators and WrapperFactory ---

// WrapperFactory creates a decorator on top of a Store while
// receiving its dependencies from Curator. It is applied during
// Target/Backup registration through WithStore/WithBackup. This
// resolves the dependency cycle: decorators get access to
// HostStorage and Publisher through a standard contract, not via
// public objects.
type WrapperFactory interface {
	Wrap(store store.DataStore, deps WrapperDeps) (store.DataStore, error)
}

// WrapperDeps are the dependencies provided by Curator to a
// decorator at registration time. HostStorage may be nil if no
// transit buffer has been registered with Curator; the decorator
// is responsible for checking this and returning an error if it
// requires HostStorage.
type WrapperDeps struct {
	HostStorage host.TransitStore
	Publisher   event.Publisher
}
