// Package config is the store-configuration model (ADR-110): the field
// registry — one declaration per StoreConfig field carrying its class,
// connection behaviour, validator and default (registry.go) — and the
// operations derived from it: defaults (ApplyDefaults), validation
// (ValidateImmutable, ValidateAgainstActive), the connection plan
// (PlanConnection) and the session overlay merge (MergeSession). Every
// consumer of the StoreConfig axis — the engine, the assembly, future
// CLI/Explain tooling — validates and presents configuration through
// this package.
//
// The machinery that turns those declarations into operations — the
// typed descriptor, the validator constructors, the traversal engine —
// lives in the internal subpackage fieldkit and is never edited when
// adding a field. To add a config field you add one row to the registry
// (and its struct field in config.StoreConfig); nothing in fieldkit.
//
// Neighbouring packages own the other two concerns: the YAML/JSON
// document that an operator writes and its mapping onto a StoreConfig
// live in config/declarative; the persistence of config.StoreConfig versions
// lives in engine/store (config_persist.go, plumbing over named cells).
package config
