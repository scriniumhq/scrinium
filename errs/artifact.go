package errs

import (
	"errors"
	"io/fs"
)

// Artifact-level operations: lookup, namespace policy, retention,
// deletion.

// ErrArtifactNotFound — no manifest with the given ArtifactID
// exists in the Store, or it is a headless pack container (empty
// identity slot — not a client-visible artifact). Bridges to
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

// ErrManifestTooLarge — serialised Manifest > 1 MB.
var ErrManifestTooLarge = errors.New("scrinium: manifest too large")
