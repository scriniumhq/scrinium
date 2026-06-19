// Package timefmt holds the on-disk timestamp format shared by
// index/sqlite and engine/artifact. Both write timestamps
// to durable storage and must agree on the byte-level format,
// or RebuildIndex (M3) cannot copy strings between subsystems
// without reformatting.
//
// Format is RFC 3339 with second precision (no nanoseconds),
// always UTC. Sub-second precision is dropped on Format because
// it would defeat ArtifactID dedup: identical logical content
// must produce identical IDs, and time.Now() varies below the
// second.
//
// Parse accepts the Nano variant in addition to the strict form
// for forward compatibility — if a future migration writes
// nanosecond timestamps, existing readers still work.
package timefmt
