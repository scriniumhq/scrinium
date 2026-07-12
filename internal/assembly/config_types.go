package assembly

import "scrinium.dev/config"

// The declarative configuration model moved to the public top-level
// package config — the single entry point of the high-level
// configuration axis (assembly is a consumer, like everyone else).
// These aliases keep the assembly's internal wiring and the facade
// re-exports source-compatible; they are the SAME types.
type (
	Config         = config.Config
	Defaults       = config.Defaults
	Projection     = config.Projection
	StoreSpec      = config.StoreSpec
	Credentials    = config.Credentials
	Policy         = config.Policy
	Encryption     = config.Encryption
	Chunking       = config.Chunking
	Bundling       = config.Bundling
	MultistoreSpec = config.MultistoreSpec
	Schedule       = config.Schedule
	ScrubSchedule  = config.ScrubSchedule
	AgentSpec      = config.AgentSpec
	PipelineStage  = config.PipelineStage
	Size           = config.Size
	Duration       = config.Duration
)
