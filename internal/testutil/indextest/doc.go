// Package indextest is the shared conformance suite for
// implementations of store.StoreIndex.
//
// Every implementation (engine/index/sqlite, future engine/index/postgres,
// future in-memory backends) is expected to register a Factory
// and call Run from its own _test.go. The suite exercises the
// public StoreIndex contract through black-box assertions only —
// no SQL, no implementation-specific table peeks.
//
// Tests that require glass-box visibility (verifying a SQL
// transaction rolled back, that a particular SQLITE_BUSY mapping
// returned the right errs sentinel, that NULL columns are handled
// the way the schema expects) stay in the implementation
// subpackage. They are not duplicates of conformance tests; they
// witness the same property through a stricter mechanism.
//
// Usage:
//
//	func TestConformance_SQLite(t *testing.T) {
//	    indextest.Run(t, indextest.Factory{
//	        Name: "sqlite-memory",
//	        New: func(t *testing.T) core.StoreIndex {
//	            idx, err := sqlite.NewStore(context.Background(), ":memory:")
//	            if err != nil { t.Fatal(err) }
//	            t.Cleanup(func() { _ = idx.Close() })
//	            return idx
//	        },
//	    })
//	}
package indextest
