// Package domain holds the value types of the Scrinium engine —
// artifacts, manifests, physical addresses, configuration enums.
// They are pure data: structs and typed strings, no methods that
// do I/O, no imports beyond the Go standard library.
//
// Why a separate package: helpers under internal/* (blobpath,
// the manifest codec, future codecs) need these types. If they lived in
// store/, the helpers would import store, and store imports the
// helpers — a cycle. Pulling the value types one level below
// breaks the cycle by making the dependency one-way:
//
//	domain  ← store  ← internal/lib
//	   ↑               ↑
//	   └───────────────┘  (helpers also import domain)
//
// User code imports both: the verb surface ("scrinium"/engine/store —
// InitStore, OpenStore, options, pipeline stages, events) and "domain"
// for the values passed into them (StoreConfig, PutOptions, Manifest,
// the policy enums). The convention follows database/sql ↔
// database/sql/driver and net/http ↔ net/url in the standard library.
//
// DAG: domain imports nothing from the project. It is the leaf.
package domain
