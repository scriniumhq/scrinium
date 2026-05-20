package vfs

import (
	"os"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/projection"
	"scrinium.dev/engine/projection/fsmeta"
)

// --- os.FileInfo adapters ---

// projectionFileInfo wraps a projection.FileInfo as os.FileInfo.
type projectionFileInfo struct {
	fi projection.FileInfo
}

func (p projectionFileInfo) Name() string       { return p.fi.Name }
func (p projectionFileInfo) Size() int64        { return p.fi.Size }
func (p projectionFileInfo) Mode() os.FileMode  { return modeFromUint32(p.fi.Mode, p.fi.IsDir) }
func (p projectionFileInfo) ModTime() time.Time { return p.fi.ModTime }
func (p projectionFileInfo) IsDir() bool        { return p.fi.IsDir }
func (p projectionFileInfo) Sys() any           { return nil }

// ArtifactID surfaces the underlying artifact id when one is
// known. Empty for virtual directories. Surfaces (web view)
// type-assert this method to discover info-link targets.
func (p projectionFileInfo) ArtifactID() domain.ArtifactID { return p.fi.ArtifactID }

// MIME surfaces the fsmeta-recorded MIME type. Surfaces use
// it to decide whether to advertise an inline [view] link for
// the row.
func (p projectionFileInfo) MIME() string { return p.fi.MIME }

// projectionNodeInfo wraps a projection.Node as os.FileInfo
// for the service-tree side. POSIX attributes are best-effort:
// the service trees do not run through FSOps so fsmeta is not
// decoded — we surface 0o555 for dirs and 0o444 for files.
type projectionNodeInfo struct {
	node         projection.Node
	fallbackTime time.Time
}

func (p projectionNodeInfo) Name() string { return p.node.FS.Name }
func (p projectionNodeInfo) Size() int64  { return p.node.FS.Size }
func (p projectionNodeInfo) Mode() os.FileMode {
	if p.node.FS.IsDir {
		return os.ModeDir | 0o555
	}
	return 0o444
}
func (p projectionNodeInfo) ModTime() time.Time {
	return nodeModTime(p.node, p.fallbackTime)
}
func (p projectionNodeInfo) IsDir() bool { return p.node.FS.IsDir }
func (p projectionNodeInfo) Sys() any    { return nil }

// ArtifactID surfaces the underlying artifact id for service-
// tree files (e.g. _scrinium/orphaned/.../<id>). Empty for
// virtual directories along the service path.
func (p projectionNodeInfo) ArtifactID() domain.ArtifactID {
	if p.node.Artifact == nil {
		return ""
	}
	return p.node.Artifact.ArtifactID
}

// MIME decodes the fsmeta payload of the underlying artifact
// and returns its MIME field. Empty when the artifact has no
// fsmeta (or any decode failure) — surfaces use that as the
// cue to omit the [view] button.
func (p projectionNodeInfo) MIME() string {
	if p.node.Artifact == nil {
		return ""
	}
	fs, ok, err := fsmeta.Decode(p.node.Artifact.Ext)
	if err != nil || !ok {
		return ""
	}
	return fs.MIME
}

// synthDirInfo / synthFileInfo are quick os.FileInfo for
// virtual directories (service prefix root) and virtual files
// (stats).
type synthInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (s synthInfo) Name() string       { return s.name }
func (s synthInfo) Size() int64        { return s.size }
func (s synthInfo) Mode() os.FileMode  { return s.mode }
func (s synthInfo) ModTime() time.Time { return s.modTime }
func (s synthInfo) IsDir() bool        { return s.isDir }
func (s synthInfo) Sys() any           { return nil }

func synthDirInfo(name string, t time.Time) os.FileInfo {
	return synthInfo{name: name, mode: os.ModeDir | 0o555, modTime: t, isDir: true}
}

func synthFileInfo(name string, size int64, t time.Time) os.FileInfo {
	return synthInfo{name: name, size: size, mode: 0o444, modTime: t}
}

// modeFromUint32 produces os.FileMode from a POSIX mode plus
// an IsDir flag. Mirrors os.FileInfo conventions.
func modeFromUint32(m uint32, isDir bool) os.FileMode {
	mode := os.FileMode(m & 0o7777)
	if isDir {
		mode |= os.ModeDir
	}
	return mode
}
