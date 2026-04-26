// Package domain holds the value types of the Scrinium engine —
// artifacts, manifests, physical addresses, configuration enums.
// They are pure data: structs and typed strings, no methods that
// do I/O, no imports beyond the Go standard library.
//
// Why a separate package: helpers under internal/* (blobpath,
// manifestcodec, future codecs) need these types. If they lived in
// core/, the helpers would import core, and core imports the
// helpers — a cycle. Pulling the value types one level below
// breaks the cycle by making the dependency one-way:
//
//	domain  ← core  ← internal/lib
//	   ↑              ↑
//	   └──────────────┘  (helpers also import domain)
//
// core re-exports every type from this package as an alias, so the
// rest of the codebase keeps importing "core" as before. New code
// can import "domain" directly when it only needs value types.
//
// DAG: domain imports nothing from the project. It is the leaf.
package domain
