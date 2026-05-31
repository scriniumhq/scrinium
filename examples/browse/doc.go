// browse opens an existing Scrinium store read-only and prints a
// summary: total artifact count, total bytes referenced, per-namespace
// breakdown, and capacity from the underlying driver.
//
// Demonstrates:
//
//   - Assembling a store read-only from a configuration
//     (projection.readOnly: true — no writes, no scratch dir needed).
//   - Iterating manifests via Store.Walk.
//   - Pulling capacity figures via Store.Capacity.
//   - Listing index extensions via ScriniumClient.Extensions().
//
// Usage:
//
//	go run ./browse --store=/tmp/my-store
package main
