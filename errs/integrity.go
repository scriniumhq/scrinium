package errs

import "errors"

// Integrity and format compatibility: hashes that don't match,
// schema versions a binary cannot read, unrecognised pack magic.
// Surfaced by engine/artifact verification, by Get on read, and by
// the Scrub Agent.

// ErrCorruptedManifest — the hash of the manifest file does not
// match its ArtifactID.
var ErrCorruptedManifest = errors.New("scrinium: corrupted manifest")

// ErrCorruptedBlob — the hash of the physical blob does not match
// its BlobRef.
var ErrCorruptedBlob = errors.New("scrinium: corrupted blob")

// ErrCorruptedContent — after the inverse Pipeline the hash does
// not match ContentHash.
var ErrCorruptedContent = errors.New("scrinium: corrupted content")

// ErrUnsupportedSchemaVersion — the manifest's schema_version is
// not supported by the running binary.
var ErrUnsupportedSchemaVersion = errors.New("scrinium: unsupported schema version")

// ErrUnknownPackFormat — the magic bytes of a .pack volume are
// unrecognised.
var ErrUnknownPackFormat = errors.New("scrinium: unknown pack format")
