// Package fspath is an index custom index that persists the vfsmeta
// payload of every artifact whose Manifest.Ext uses the
// filesystem schema. It hangs off StoreIndex via the index
// custom indexes infrastructure (see 3. Reference/09 CustomIndex and Search.md).
//
// The custom index serves two roles:
//
//   - Backfill source for view.View. After a process
//     restart the View needs to rebuild its filesystem trees
//     from indexed metadata; without fspathindex it would fall
//     back to N+1 round-trips through Source.Get to re-read
//     each manifest's Ext. fspathindex persists those bytes
//     once at write time, so backfill is a single bulk scan.
//
//   - Direct path lookup. Hosts that want to translate a
//     virtual path to an ArtifactID without standing up a
//     full View (FUSE Stat hot-path, WebDAV PROPFIND on a
//     specific resource) call LookupByPath. The custom index
//     keeps a reverse index for O(log N) lookups.
//
// fspathindex stores the vfsmeta JSON as-is rather than pre-decoded
// columns. The marker schema is versioned (`scrinium.fs/v1`,
// future v2…); keeping the bytes verbatim lets newer schemas
// flow through without an fspathindex migration whenever vfsmeta
// adds a field.
package fspath
