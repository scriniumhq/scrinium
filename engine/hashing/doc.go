// Package hashing provides the default domain.HashRegistry
// implementation: the content-hash registry that parses, formats, and
// constructs hashers for the "<algo>-<hex>" identifiers used
// throughout Scrinium (ContentHash, BlobRef, ArtifactID).
//
// Content hashing is its own concern, separate from the transformer
// pipeline and key resolution. The host application registers the
// algorithms it supports (sha256, blake3, …) at wiring time via
// Register.
package hashing
