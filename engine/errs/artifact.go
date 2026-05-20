package errs

import (
	"errors"
	"io/fs"
)

// Artifact-level operations: lookup, namespace policy, retention,
// deletion. See docs/2. Internals/02 §2.2 (Delete flow), §2.4
// (Get path), docs/2. Internals/07 §7.2.2 (RetentionUntil).

// ErrArtifactNotFound — no manifest with the given ArtifactID
// exists in the Store, or it is a ManifestTypePack (an internal
// type that does not exist for the client). Bridges to
// fs.ErrNotExist for host code that handles missing artifacts
// the same way as missing files.
var ErrArtifactNotFound = newBridgedSentinel(
	"scrinium: artifact not found", fs.ErrNotExist,
)

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

// ErrExtTooLarge — Artifact.Ext > MaxExtSize (64 KB). Ext is the
// engine-readable extension block (fsmeta and friends).
var ErrExtTooLarge = errors.New("scrinium: ext metadata too large")

// ErrUsrTooLarge — Artifact.Usr > MaxUsrSize (64 KB). Usr is the
// opaque host-application block.
var ErrUsrTooLarge = errors.New("scrinium: usr metadata too large")

// ErrMetadataTooLarge — Artifact.Metadata > 64 KB.
//
// Deprecated: split into ErrExtTooLarge / ErrUsrTooLarge per
// ADR-54. Kept during the migration; removed in R2b together
// with the Metadata field.
var ErrMetadataTooLarge = errors.New("scrinium: metadata too large")

// ErrManifestTooLarge — serialised Manifest > 1 MB.
var ErrManifestTooLarge = errors.New("scrinium: manifest too large")
