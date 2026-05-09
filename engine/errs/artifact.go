package errs

import "errors"

// Artifact-level operations: lookup, namespace policy, retention,
// deletion. See docs/2. Internals/02 §2.2 (Delete flow), §2.4
// (Get path), docs/2. Internals/07 §7.2.2 (RetentionUntil).

// ErrArtifactNotFound — no manifest with the given ArtifactID
// exists in the Store, or it is a ManifestTypePack (an internal
// type that does not exist for the client).
var ErrArtifactNotFound = errors.New("scrinium: artifact not found")

// ErrDeletionForbidden — Delete on a Store with
// DeletionPolicy: NoDelete.
var ErrDeletionForbidden = errors.New("scrinium: deletion forbidden")

// ErrRetentionNotExpired — Delete or RollbackSession on an artifact
// with an active RetentionUntil.
var ErrRetentionNotExpired = errors.New("scrinium: retention not expired")

// ErrArchivedArtifact — the artifact is reachable only through a
// Backup with ReadPolicy: Never; AllowColdRead is required.
var ErrArchivedArtifact = errors.New("scrinium: archived artifact")

// ErrReservedNamespace — an attempt to use "*" or the "system."
// prefix without a CapabilityToken.
var ErrReservedNamespace = errors.New("scrinium: reserved namespace")

// ErrNamespaceTooLong — namespace > 255 bytes.
var ErrNamespaceTooLong = errors.New("scrinium: namespace too long")

// ErrSessionIDTooLong — SessionID > 255 bytes.
var ErrSessionIDTooLong = errors.New("scrinium: session ID too long")

// ErrEmptySessionID — RollbackSession called with an empty string;
// guards against a mass deletion of sessionless artifacts.
var ErrEmptySessionID = errors.New("scrinium: empty session ID")

// ErrMetadataTooLarge — Artifact.Metadata > 64 KB.
var ErrMetadataTooLarge = errors.New("scrinium: metadata too large")

// ErrManifestTooLarge — serialised Manifest > 1 MB.
var ErrManifestTooLarge = errors.New("scrinium: manifest too large")
