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

// byArtifactPath: <aa>/<bb>/<full-id>
func byArtifactPath(id domain.ArtifactID) string {
	hash := hashPart(string(id))
	if len(hash) < 4 {
		return "_short/" + string(id)
	}
	return hash[:2] + "/" + hash[2:4] + "/" + string(id)
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
	t := m.CreatedAt.UTC()
	name := byDateLabel(m)
	return fmt.Sprintf("%04d/%02d/%02d/%02d-%02d-%02d-%s",
		t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(),
		name)
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
	if len(s) < 4 {
		return "_short/" + s
	}
	return s[:2] + "/" + s[2:4] + "/" + s
}

// shortID returns the first 16 hex characters of the hash part of
// an ArtifactID. Used by by-date filenames and synthetic paths.
func shortID(id domain.ArtifactID) string {
	hash := hashPart(string(id))
	if len(hash) > 16 {
		return hash[:16]
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
	s = append(s, "")
	copy(s[idx+1:], s[idx:])
	s[idx] = name
	return s
}

// removeSorted removes name from a sorted slice.
func removeSorted(s []string, name string) []string {
	idx, found := slices.BinarySearch(s, name)
	if !found {
		return s
	}
	return append(s[:idx], s[idx+1:]...)
}
