// Package config is the single entry point for Scrinium's high-level
// store configuration (ADR-110): the field classification (spec.go),
// defaults (ApplyDefaults), validation (ValidateImmutable,
// ValidateAgainstActive), the connection plan (PlanConnection) and the
// session overlay merge (MergeSession). Every consumer of the
// StoreConfig axis — the engine, the assembly, future CLI/Explain
// tooling — assembles, validates and presents configuration through
// this package.
//
// The declarative model lives here too: the Config/Policy file shape
// (declarative.go), strict decoding (DecodeYAML/DecodeJSON), the
// defaults ladder and policyRef resolution (Normalize), file
// validation (Validate — through the same vocabulary tables the
// mapper reads, plus the engine validator on the mapped result), the
// YAML↔domain dictionary and the policy mapping
// (StoreConfigFromPolicy) with the feature gates
// (GuardUnsupportedPolicy). The assembly consumes all of it and keeps
// only its own job — wiring components.
//
// What deliberately does NOT live here: the persistence of
// store.config versions (engine/store/internal/storeconfig — engine
// plumbing over named cells; how the store keeps its config on disk is
// the store's business), and the runtime construction of components
// (agents, projection, adapters) from the declarative blocks — that is
// the assembly's wiring.
package config
