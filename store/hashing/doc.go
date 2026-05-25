// Package hashing provides the default domain.HashRegistry
// implementation: the content-hash registry that parses, formats, and
// constructs hashers for the "<algo>-<hex>" identifiers used
// throughout Scrinium (ContentHash, BlobRef, ArtifactID).
//
// It was split out of the former grab-bag `plugins` package — content
// hashing is its own concern, unrelated to the transformer pipeline or
// key resolution that also lived there. The host application registers
// the algorithms it supports (sha256, blake3, …) at wiring time via
// Register.
package hashing
