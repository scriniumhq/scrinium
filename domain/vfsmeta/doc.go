// Package vfsmeta is the standard filesystem schema for
// Manifest.Ext. It defines a structure that maps each artifact
// to a POSIX-shaped node (path, mode, owner, mtime, MIME) and the
// encoder/decoder/resolver that ingester and projection-side
// components use to read it.
//
// Compatibility: the schema marker is "scrinium.fs/v1" and is
// stable. A future v2 will coexist (Decode picks by marker); a
// store may contain a mix of versions.
//
// Specification: docs/3 §5.5, docs/4 §13.4.2.
package vfsmeta
