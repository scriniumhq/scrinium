// Package storesuite is the black-box integration and conformance suite
// for engine/store. It contains no production code: every file here is a
// test that drives the Store through its public API only
// (scrinium.dev/engine/store), using the construction fixtures in
// testutil/storefx and the assertion/inspection helpers in
// testutil/storekit.
//
// Why a separate package. The store directory had ~9.5k lines of tests
// against ~3k lines of production code; the bulk were black-box
// (package store_test) suites that touch nothing unexported. Pulling them
// here keeps engine/store itself lean — only the production files, the
// four internal (package store) tests that need unexported access, and
// the handful of external tests anchored on export_test.go helpers remain
// in that directory.
//
// What stays in engine/store instead of moving here:
//   - tests that need unexported access (package store + export_test.go);
//   - external tests that use the *store-internal* export_test.go helpers
//     (StoreKeyResolver, ReadDriverFile, WriteDriverFile), which cannot be
//     reached from another package.
package storesuite
