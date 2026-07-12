// Package config is the single entry point for Scrinium's high-level
// store configuration (ADR-110): the field classification (spec.go),
// defaults (ApplyDefaults), validation (ValidateImmutable,
// ValidateAgainstActive), the connection plan (PlanConnection) and the
// session overlay merge (MergeSession). Every consumer of the
// StoreConfig axis — the engine, the assembly, future CLI/Explain
// tooling — assembles, validates and presents configuration through
// this package.
//
// What deliberately does NOT live here: the persistence of
// store.config versions (engine/store/internal/storeconfig — engine
// plumbing over named cells; how the store keeps its config on disk is
// the store's business), and component configs (agents, projection,
// adapter routing) — those are a different axis, owned by the
// declarative assembly model.
package config
