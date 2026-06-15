// Package fsindex is an index extension that persists the fsmeta
// payload of every artifact whose Manifest.Ext uses the
// filesystem schema. It hangs off StoreIndex via the index
// extensions infrastructure (see 3. Reference/09 CustomIndex and Search.md).
//
// The extension serves two roles:
//
//   - Backfill source for view.View. After a process
//     restart the View needs to rebuild its filesystem trees
//     from indexed metadata; without fsindex it would fall
//     back to N+1 round-trips through Source.Get to re-read
//     each manifest's Ext. fsindex persists those bytes
//     once at write time, so backfill is a single bulk scan.
//
//   - Direct path lookup. Hosts that want to translate a
//     virtual path to an ArtifactID without standing up a
//     full View (FUSE Stat hot-path, WebDAV PROPFIND on a
//     specific resource) call LookupByPath. The extension
//     keeps a reverse index for O(log N) lookups.
//
// fsindex stores the fsmeta JSON as-is rather than pre-decoded
// columns. The marker schema is versioned (`scrinium.fs/v1`,
// future v2…); keeping the bytes verbatim lets newer schemas
// flow through without an fsindex migration whenever fsmeta
// adds a field.
package fs
