// Package errs holds every sentinel error of the Scrinium engine.
// One package, one source of truth — anything matched via
// errors.Is/errors.As lives here.
//
// Why a flat shared package and not per-layer files (core/errors.go,
// domain/errors.go, driver/driver.go) — sentinels are part of the
// public contract, not of the implementation that returns them.
// `Walk`'s ErrStopWalk is the same idea regardless of whether it
// surfaces from the storage core or from a driver-level list call;
// `ErrUnsupportedURIScheme` originates in a driver but is matched by
// callers two layers up. Splitting the same identity into per-layer
// duplicates with `var X = otherpkg.X` re-exports has been tried and
// produced exactly the kind of confusion this package is meant to
// remove.
//
// Files in this package are organised by domain (manifest, store
// lifecycle, lease, etc.) so the package stays scannable as it grows.
// Callers that need only one or two errors are still fine — Go's
// import system makes the file split transparent.
//
// DAG note: errs is a leaf package, the same as event. It imports
// only stdlib. driver, domain, core all import errs; nothing imports
// the other direction.
package errs
