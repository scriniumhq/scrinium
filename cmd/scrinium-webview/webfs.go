package main

import (
	"context"
	"errors"
	"io"
	"os"
	pathpkg "path"
	"scrinium.dev/projection"

	"scrinium.dev/cmd/scrinium-webview/web"
	"scrinium.dev/domain"
	"scrinium.dev/domain/fsmeta"
	"scrinium.dev/projection/vfs"
)

// webBackingFS adapts vfs.VFS to web.BackingFS.
// Read-only by design — webview never invokes write paths.
//
// The webdav cmd has a similar adapter that goes through its
// webdav-shaped FileSystem because that surface needs the
// junk filter and locking machinery; webview talks to vfs
// directly because its only consumer is HTML rendering.
type webBackingFS struct {
	v      *vfs.VFS
	reader projection.Reader
}

func newWebBackingFS(v *vfs.VFS, reader projection.Reader) *webBackingFS {
	return &webBackingFS{v: v, reader: reader}
}

func (b *webBackingFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return b.v.Stat(ctx, name)
}

func (b *webBackingFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (web.File, error) {
	f, err := b.v.OpenFile(ctx, name, flag, perm)
	if err != nil {
		return nil, err
	}
	// vfs.File and web.File share Read/Write/Seek/Close +
	// Readdir + Stat — Go's structural subtyping does the
	// conversion automatically.
	return f, nil
}

// LookupManifest fetches the manifest by id through the
// projection. Open-and-close pattern: web only needs the
// manifest, not bytes.
func (b *webBackingFS) LookupManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, bool, error) {
	rh, err := b.reader.Open(ctx, id)
	if err != nil {
		// "Not found" and infrastructure errors aren't
		// distinguished here; treat both as "not found"
		// since the page degrades gracefully either way.
		return domain.Manifest{}, false, nil
	}
	defer rh.Close()
	return rh.Manifest(), true, nil
}

// OpenArtifact opens artifact bytes by id. Used by /_view
// and /_download endpoints which don't have a path.
func (b *webBackingFS) OpenArtifact(ctx context.Context, id domain.ArtifactID) (web.File, web.ArtifactMeta, error) {
	rh, err := b.reader.Open(ctx, id)
	if err != nil {
		return nil, web.ArtifactMeta{}, err
	}
	m := rh.Manifest()
	name := ""
	mimeType := ""
	if fs, ok, err := fsmeta.Decode(m.Ext); err == nil && ok {
		name = pathpkg.Base(fs.Path)
		mimeType = fs.MIME
	}
	return &readHandleAdapter{rh: rh, size: m.OriginalSize}, web.ArtifactMeta{
		Name:    name,
		MIME:    mimeType,
		ModTime: m.CreatedAt,
	}, nil
}

// readHandleAdapter wraps a domain.ReadHandle so it satisfies
// web.File. Same shape as the webdav-cmd version — kept here
// rather than in shared web because the type is glue
// between core and the web pkg, owned by each cmd.
type readHandleAdapter struct {
	rh   domain.ReadHandle
	pos  int64
	size int64
}

func (a *readHandleAdapter) Read(p []byte) (int, error) {
	if a.size > 0 && a.pos >= a.size {
		return 0, io.EOF
	}
	n, err := a.rh.ReadAt(p, a.pos)
	a.pos += int64(n)
	return n, err
}

func (a *readHandleAdapter) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = a.pos + offset
	case io.SeekEnd:
		abs = a.size + offset
	default:
		return 0, errors.New("readHandleAdapter.Seek: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("readHandleAdapter.Seek: negative position")
	}
	a.pos = abs
	return abs, nil
}

func (a *readHandleAdapter) Write(p []byte) (int, error) {
	return 0, errors.New("readHandleAdapter: read-only")
}

func (a *readHandleAdapter) Close() error                       { return a.rh.Close() }
func (a *readHandleAdapter) Readdir(int) ([]os.FileInfo, error) { return nil, nil }
func (a *readHandleAdapter) Stat() (os.FileInfo, error)         { return nil, nil }

// LookupRelated walks the View for artifacts pointing at
// the same blob.
func (b *webBackingFS) LookupRelated(ctx context.Context, blobRef domain.BlobRef, exclude domain.ArtifactID) ([]projection.RelatedArtifact, error) {
	siblings := b.reader.RelatedByBlobRef(blobRef, exclude)
	out := make([]projection.RelatedArtifact, 0, len(siblings))
	for _, s := range siblings {
		out = append(out, projection.RelatedArtifact{
			ArtifactID: s.ArtifactID,
			Path:       s.Path,
			Namespace:  s.Namespace,
			SessionID:  s.SessionID,
			CreatedAt:  s.CreatedAt,
		})
	}
	return out, nil
}

// Search proxies to the View's text search.
func (b *webBackingFS) Search(ctx context.Context, query string, limit int) ([]projection.SearchResult, error) {
	hits := b.reader.Search(query, limit)
	out := make([]projection.SearchResult, 0, len(hits))
	for _, h := range hits {
		out = append(out, projection.SearchResult{
			ArtifactID:  h.ArtifactID,
			Path:        h.Path,
			Namespace:   h.Namespace,
			SessionID:   h.SessionID,
			CreatedAt:   h.CreatedAt,
			MIME:        h.MIME,
			MatchReason: h.MatchReason,
		})
	}
	return out, nil
}

// LookupLocations returns the per-tree placement of an
// artifact for the Locations panel.
func (b *webBackingFS) LookupLocations(ctx context.Context, id domain.ArtifactID) (projection.Locations, bool, error) {
	locs, ok := b.reader.LookupLocations(id)
	if !ok {
		return projection.Locations{}, false, nil
	}
	return projection.Locations{
		ByArtifact:  locs.ByArtifact,
		BySession:   locs.BySession,
		ByNamespace: locs.ByNamespace,
		ByDate:      locs.ByDate,
		ByPath:      locs.ByPath,
		ByOrphaned:  locs.ByOrphaned,
	}, true, nil
}
