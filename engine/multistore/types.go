package multistore

import (
	"scrinium.dev/domain"
)

// --- Policy enums ---

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
	// multistore.Store(backupID).Get.
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
// Target Store with the multistore via WithStore.
type StoreRegistrationConfig struct {
	Priority             int
	ReadCost             ReadCost
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
// The multistore builds it from PutOptions.
type RoutingMetadata struct {
	Namespace   string
	Size        int64
	ContentType string
	Source      string
	Attributes  map[string]string
}

// RoutingFunc selects Target Stores for a write through the
// multistore. It can be compiled from declarative configuration
// rules or supplied directly by the developer.
type RoutingFunc func(meta RoutingMetadata) []StoreTarget

// MetadataRouter reconstructs RoutingHints from the manifest
// (Namespace, Ext, Usr). Used when re-routing already-written
// artifacts as a separate operation, when the original hints from
// PutOptions are no longer available.
type MetadataRouter func(m domain.Manifest) domain.RoutingHints
