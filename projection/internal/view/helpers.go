package view

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/projection/pathx"
)

// newDirNode creates an empty virtual-directory node. POSIX
// mode/uid/gid live in FSOps defaults — virtual directories
func newDirNode(name, path string, modTime time.Time) *viewNode {
	return &viewNode{
		fs: FilesystemFacet{
			Name:    name,
			Path:    path,
			IsDir:   true,
			ModTime: modTime,
		},
	}
}

// artifactFacetFrom builds the Node.Artifact facet from a manifest.
func artifactFacetFrom(m domain.Manifest) *ArtifactFacet {
	return &ArtifactFacet{
		ArtifactID:  m.ArtifactID,
		ContentHash: m.ContentHash,
		BlobRef:     m.PrimaryBlobRef(),
		SessionID:   m.SessionID,
		CreatedAt:   m.CreatedAt,
		Ext:         m.Ext,
	}
}

// exportNode builds the public Node from the internal viewNode.
// Caller holds the read lock.
func (v *View) exportNode(n *viewNode) Node {
	out := Node{FS: n.fs}
	if n.artifact != nil {
		af := *n.artifact
		out.Artifact = &af
	}
	return out
}

// publish emits an event when an EventBus is configured. Keeps
// callers' code path event-agnostic.
func (v *View) publish(eventType string, payload any) {
	if v.bus == nil {
		return
	}
	v.bus.Publish(event.Event{Type: eventType, Payload: payload})
}

// emit publishes a batch of events collected during a locked mutation.
//
// It MUST be called only after v.mu has been released: the default bus
// (event.NewEventBus) delivers synchronously on the calling goroutine, so
// a subscriber that reads the View from its handler would self-deadlock if
// emit ran while the write lock was still held. Mutators accumulate
// collision events into a local slice under the lock and hand it here once
// unlocked — see Add/Move/applyDelta.
func (v *View) emit(events []event.Event) {
	if v.bus == nil {
		return
	}
	for _, e := range events {
		v.bus.Publish(e)
	}
}

// --- Error mapping ---

func mapSourceError(err error) error {
	switch {
	case errors.Is(err, errs.ErrArtifactNotFound):
		return fmt.Errorf("%w: %w", errs.ErrPathNotFound, err)
	case errors.Is(err, errs.ErrLocked),
		errors.Is(err, errs.ErrCorruptedManifest),
		errors.Is(err, errs.ErrCorruptedBlob):
		return fmt.Errorf("%w: %w", errs.ErrArtifactUnreadable, err)
	default:
		return fmt.Errorf("%w: %w", errs.ErrSourceUnavailable, err)
	}
}

// --- Path-building helpers ---

// Path-sharding geometry. The by-artifact and by-session trees shard on a
// two-level <aa>/<bb> prefix of the hex hash — each segment shardSegmentLen
// wide — so entries fan out across 256×256 directories; identifiers shorter
// than the combined shardPrefixLen bucket under "_short/". shortIDLen is the
// hex width of the short id used in by-date filenames and synthetic paths.
const (
	shardSegmentLen = 2
	shardPrefixLen  = 2 * shardSegmentLen
	shortIDLen      = 16
)

// byArtifactPath: <aa>/<bb>/<full-id>
func byArtifactPath(id domain.ArtifactID) string {
	hash := hashPart(string(id))
	if len(hash) < shardPrefixLen {
		return "_short/" + string(id)
	}
	return hash[:shardSegmentLen] + "/" + hash[shardSegmentLen:shardPrefixLen] + "/" + string(id)
}

// byDatePath: <YYYY>/<MM>/<DD>/<HH-MM-SS>-<id-short>.bin
// byDatePath builds the by-date layout: <YYYY>/<MM>/<DD>/<HH-MM-SS>-<name>.
// The trailing name is the vfsmeta path's basename when available
// (so the listing shows "12-34-56-sunset.jpg") or a short artifact
// id otherwise (for non-vfsmeta artifacts that have no human name).
//
// Time resolution is 1 second; same-second artifacts get a dash-id
// suffix appended via the basename which is always unique.
func byDatePath(m domain.Manifest) string {
	return m.CreatedAt.UTC().Format("2006/01/02/15-04-05-") + byDateLabel(m)
}

// byDateLabel picks the human-friendly suffix for a by-date path.
// Priority: vfsmeta path basename → short artifact id with ".bin"
// custom index. Two artifacts created in the same second with the
// same vfsmeta basename collide; that's accepted — the by-date
// tree is a diagnostic aid, not an authoritative storage layout.
func byDateLabel(m domain.Manifest) string {
	if fs, ok, err := vfsmeta.Decode(m.Ext); err == nil && ok {
		base := pathx.LastSegment(fs.Path)
		if base != "" {
			return base
		}
	}
	return shortID(m.ArtifactID) + ".bin"
}

// bySessionPath: <aa>/<bb>/<sid>/<artifact-id>
//
// Sessions shorter than 4 characters bucket under "_short/<sid>/...".
// Caller must check m.SessionID != "" before calling.
func bySessionPath(m domain.Manifest) string {
	// Flat layout: <session>/<artifactID>. Earlier versions
	// sharded like by-artifact (xx/yy/sid/...) for forward
	// scalability, but in practice session counts stay tiny
	// (one per process restart) and the sharding only
	// obscured the listing for human inspection.
	sid := string(m.SessionID)
	if sid == "" {
		// Defensive: callers gate this with m.SessionID != ""
		// before invoking, but guard against drift.
		sid = "_no_session"
	}
	return sid + "/" + string(m.ArtifactID)
}

// sessionShard returns the first-segment shard for a SessionID.
// Used by syntheticPath; format mirrors bySessionPath's prefix.
func sessionShard(sid domain.SessionID) string {
	s := string(sid)
	if len(s) < shardPrefixLen {
		return "_short/" + s
	}
	return s[:shardSegmentLen] + "/" + s[shardSegmentLen:shardPrefixLen] + "/" + s
}

// shortID returns the leading shortIDLen hex characters of the hash part
// of an ArtifactID. Used by by-date filenames and synthetic paths.
func shortID(id domain.ArtifactID) string {
	hash := hashPart(string(id))
	if len(hash) > shortIDLen {
		return hash[:shortIDLen]
	}
	return hash
}

// hashPart strips the algorithm prefix from an identifier of
// the form "<algo>-<hex>".
func hashPart(id string) string {
	if i := strings.IndexByte(id, '-'); i >= 0 {
		return id[i+1:]
	}
	return id
}

// insertSorted inserts name into a sorted slice (idempotent).
func insertSorted(s []string, name string) []string {
	idx, found := slices.BinarySearch(s, name)
	if found {
		return s
	}
	return slices.Insert(s, idx, name)
}

// removeSorted removes name from a sorted slice.
func removeSorted(s []string, name string) []string {
	idx, found := slices.BinarySearch(s, name)
	if !found {
		return s
	}
	return slices.Delete(s, idx, idx+1)
}
