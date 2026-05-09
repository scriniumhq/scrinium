// Package indexfx supplies StoreIndex fixtures for tests.
//
// Memory returns an in-memory sqlite-backed StoreIndex (file:::memory:),
// the fastest option for tests that need a real-shaped index but
// don't care about persistence.
//
// Disk returns a sqlite-backed StoreIndex at the given file path,
// for tests that exercise reopen / persistence behaviour.
//
// Both register a t.Cleanup that closes the index — tests don't
// have to remember.
//
// In-package tests of engine/index/sqlite use their own helpers
// because they need access to the package-private *Index type
// that this package can't expose through the core.StoreIndex
// interface.
package indexfx
