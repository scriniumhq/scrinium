package host

import "time"

// --- Configuration enums ---

// EvictionPolicy is the eviction policy of HostStorage.
type EvictionPolicy string

const (
	EvictionPolicyLRU      EvictionPolicy = "LRU"
	EvictionPolicyTTL      EvictionPolicy = "TTL"
	EvictionPolicyPressure EvictionPolicy = "Pressure"
)

// OnHostStorageFull controls behaviour when the HostStorage hard
// limit is hit.
type OnHostStorageFull string

const (
	OnHostStorageFullBlock        OnHostStorageFull = "Block"
	OnHostStorageFullDirectStream OnHostStorageFull = "DirectStream"
	OnHostStorageFullReject       OnHostStorageFull = "Reject"
)

// OnDrainNoTarget controls behaviour when the Router returns 0
// targets at Drain time.
type OnDrainNoTarget string

const (
	OnDrainNoTargetRetain     OnDrainNoTarget = "Retain"
	OnDrainNoTargetQuarantine OnDrainNoTarget = "Quarantine"
)

// --- Configuration struct ---

// HostStorageConfig is the configuration of the transit buffer.
// WorkspaceDir is required.
type HostStorageConfig struct {
	EvictionPolicy    EvictionPolicy
	OnHostStorageFull OnHostStorageFull
	OnDrainNoTarget   OnDrainNoTarget
	SoftLimitBytes    int64
	HardLimitBytes    int64
	EventCooldown     time.Duration
	DrainInterval     time.Duration
	WorkspaceDir      string
}

// --- Snapshot struct ---

// HostStorageStats is the current physical state of the transit
// buffer. The values are a snapshot at the moment of the request.
type HostStorageStats struct {
	TransitBytes    int64 // bytes in transit (excluding quarantine)
	TransitFiles    int   // number of files awaiting Drain
	QuarantineBytes int64 // bytes in system.transit/quarantine/
	QuarantineFiles int   // number of files in quarantine
	MaxTransitBytes int64 // hard limit from configuration
}
