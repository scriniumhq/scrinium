// Package vfsmeta is the standard filesystem schema for
// Manifest.Ext. It defines a structure that maps each artifact
// to a POSIX-shaped node (path, mode, owner, mtime, MIME) and the
// embed/decode/resolver helpers that ingester and projection-side
// components use to read it.
//
// Shape: Manifest.Ext is a JSON object keyed by schema name; this
// schema lives under the "vfsmeta" key (the key is the schema
// discriminator). The payload carries a "version" field (currently 1).
// A future version 2 will coexist (Decode picks by version); a store
// may contain a mix of versions.
//
// Specification: docs/3 §5.5, docs/4 §13.4.2.
package vfsmeta
