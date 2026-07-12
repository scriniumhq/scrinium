package assembly

import "scrinium.dev/config/declarative"

// The declarative configuration model moved to the public top-level
// package config — the single entry point of the high-level
// configuration axis (assembly is a consumer, like everyone else).
// These aliases keep the assembly's internal wiring and the facade
// re-exports source-compatible; they are the SAME types.
type (
	Config         = declarative.Config
	Defaults       = declarative.Defaults
	Projection     = declarative.Projection
	StoreSpec      = declarative.StoreSpec
	Credentials    = declarative.Credentials
	Policy         = declarative.Policy
	Encryption     = declarative.Encryption
	Chunking       = declarative.Chunking
	Bundling       = declarative.Bundling
	MultistoreSpec = declarative.MultistoreSpec
	Schedule       = declarative.Schedule
	ScrubSchedule  = declarative.ScrubSchedule
	AgentSpec      = declarative.AgentSpec
	PipelineStage  = declarative.PipelineStage
	Size           = declarative.Size
	Duration       = declarative.Duration
)
