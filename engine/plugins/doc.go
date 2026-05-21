// Package plugins holds the engine-side implementations of the
// coreapi plugin contracts and their constructors: the transformer
// registry, the hash registry, and the default single-DEK key
// resolver. They depend only on coreapi/domain — no *store state —
// so the host wires a stack by constructing these and registering
// factories before opening a Store.
package plugins
