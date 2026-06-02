package event

import (
	"time"

	"scrinium.dev/domain"
)

// projection_payloads.go — the "projection." event-type constants and
// their payload structs, emitted by the projection layer's view
// builder. Pure data (domain types and stdlib only).
const (
	// EventPathCollision is emitted when two artifacts compete for the
	// same by-path entry; the loser stays accessible through
	// by-artifact.
	EventPathCollision = "projection.path_collision"

	// EventViewRebuilt is emitted after a successful backfill.
	EventViewRebuilt = "projection.view_rebuilt"
)

// PathCollisionPayload carries the resolution data of a path
// collision. Winner is the artifact now holding the path; Loser is the
// artifact that lost it (still reachable through by-artifact).
type PathCollisionPayload struct {
	Path   string
	Winner domain.ArtifactID
	Loser  domain.ArtifactID
}

// RebuiltPayload carries timing and counts of a backfill completion.
// NodeCount is the total number of nodes across every tree (one file
// artifact may appear under several trees).
type RebuiltPayload struct {
	Duration  time.Duration
	NodeCount int64
}
