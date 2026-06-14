package ejector

import (
	"time"
)

// EjectorConfig configures the Ejector. TempDir is required.
type EjectorConfig struct {
	// TempDir is the private per-instance scratch directory (created
	// 0700, swept on open, not shared between instances).
	TempDir string

	// MaxConcurrent bounds simultaneous materialisations (default 4).
	MaxConcurrent int

	// KeepAliveIdle reclaims a materialisation with zero holders this
	// long after its last access. 0 = never (only Close / open-sweep /
	// size-cap reclaim).
	KeepAliveIdle time.Duration

	// MaxLifetime is the absolute cap on a materialisation's age even
	// with holders > 0. 0 = never.
	MaxLifetime time.Duration

	// MaxScratchBytes caps total scratch size; on overflow, zero-holder
	// entries are evicted oldest-first. 0 = unlimited.
	MaxScratchBytes int64

	// MaxFragmentBytes rejects EjectFragment requests larger than this.
	// 0 = unlimited.
	MaxFragmentBytes int64

	// VerifyOnReuse re-hashes an existing file before reusing it; on
	// mismatch it is re-materialised. Default false (trust isolation).
	VerifyOnReuse bool
}

func (c *EjectorConfig) applyDefaults() {
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 4
	}
	// Timers default to 0 = never; the scheduler/assembler may set them.
}
