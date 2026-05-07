package main

import (
	"context"
	"errors"
	"io"
	"os"
	pathpkg "path"

	"github.com/rkurbatov/scrinium/cmd/scrinium-webdav/web"
	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// webBackingFS adapts the daemon's webdavFS to the web package's
// BackingFS interface. It's a paper-thin wrapper — the only real
// translation is OpenFile's return type, which web wants as
// web.File (a narrower interface than webdav.File). Go's
// structural subtyping does the conversion automatically once
// we say so explicitly.
type webBackingFS struct {
	wfs   *webdavFS
	store core.Store
}

func newWebBackingFS(wfs *webdavFS, store core.Store) *webBackingFS {
	return &webBackingFS{wfs: wfs, store: store}
}

func (b *webBackingFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return b.wfs.Stat(ctx, name)
}

func (b *webBackingFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (web.File, error) {
	f, err := b.wfs.OpenFile(ctx, name, flag, perm)
	if err != nil {
		return nil, err
	}
	// webdav.File is a superset of web.File (it adds Write).
	// The interface conversion is structural — every method
	// web.File requires is on f.
	return f, nil
}

// LookupManifest fetches the full manifest for an artifact id
// through the core.Store. We open and immediately close the
// ReadHandle: web only needs the manifest, not the bytes.
//
// (zero, false, nil) for "not found"; the third return covers
// driver/index errors. The handler page degrades gracefully
// either way.
func (b *webBackingFS) LookupManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, bool, error) {
	rh, err := b.store.Get(ctx, id, domain.GetOptions{})
	if err != nil {
		// Distinguishing "not found" from infrastructure
		// errors here is awkward — core.Store.Get wraps both
		// in the same error type. Treat any error as
		// "not found" for now; if we wire structured errors
		// later, branch on errs.ErrArtifactNotFound.
		return domain.Manifest{}, false, nil
	}
	defer rh.Close()
	return rh.Manifest(), true, nil
}

// OpenArtifact opens an artifact's bytes by id, returning a
// handle along with display name + MIME for the response
// headers. The ReadHandle's body satisfies web.File via
// readHandleAdapter — http.ServeContent uses Read+Seek, both
// available.
func (b *webBackingFS) OpenArtifact(ctx context.Context, id domain.ArtifactID) (web.File, web.ArtifactMeta, error) {
	rh, err := b.store.Get(ctx, id, domain.GetOptions{})
	if err != nil {
		return nil, web.ArtifactMeta{}, err
	}
	m := rh.Manifest()

	// Display name from fsmeta path; fallback handled by
	// web.serveByID when we leave it empty.
	name := ""
	mimeType := ""
	if fs, ok, err := fsmeta.Decode(m.Metadata); err == nil && ok {
		name = pathpkg.Base(fs.Path)
		mimeType = fs.MIME
	}

	return &readHandleAdapter{rh: rh, size: m.OriginalSize}, web.ArtifactMeta{
		Name:    name,
		MIME:    mimeType,
		ModTime: m.CreatedAt,
	}, nil
}

// readHandleAdapter wraps a core.ReadHandle so it satisfies
// web.File. ReadHandle provides Read/Close + ReadAt; we synthesise
// Seek via the readSeeker helper so http.ServeContent's Range
// support keeps working. Readdir/Stat are stubbed — web uses
// neither for byte streaming.
type readHandleAdapter struct {
	rh   core.ReadHandle
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

// Seek synthesises seekability over ReadHandle's ReaderAt. This
// is what http.ServeContent expects; without it Range requests
// fail with "seeker can't seek".
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

func (a *readHandleAdapter) Close() error {
	return a.rh.Close()
}

func (a *readHandleAdapter) Readdir(int) ([]os.FileInfo, error) { return nil, nil }
func (a *readHandleAdapter) Stat() (os.FileInfo, error)         { return nil, nil }

// LookupRelated walks the View for artifacts pointing at the
// same blob, excluding the caller. Linear scan inside the
// view; fast for any reasonable store. Errors aren't expected
// (the View call doesn't fail) but the interface allows for
// them, so we return nil-error.
func (b *webBackingFS) LookupRelated(ctx context.Context, blobRef domain.BlobRef, exclude domain.ArtifactID) ([]web.RelatedArtifact, error) {
	siblings := b.wfs.VFS().View().RelatedByBlobRef(blobRef, exclude)
	out := make([]web.RelatedArtifact, 0, len(siblings))
	for _, s := range siblings {
		out = append(out, web.RelatedArtifact{
			ArtifactID: s.ArtifactID,
			Path:       s.Path,
			Namespace:  s.Namespace,
			SessionID:  s.SessionID,
			CreatedAt:  s.CreatedAt,
		})
	}
	return out, nil
}

// Search proxies to the View's text search. Same linear-scan
// caveats as LookupRelated; an actual search index is a backlog
// item once the store grows beyond ~100K artifacts.
func (b *webBackingFS) Search(ctx context.Context, query string, limit int) ([]web.SearchResult, error) {
	hits := b.wfs.VFS().View().Search(query, limit)
	out := make([]web.SearchResult, 0, len(hits))
	for _, h := range hits {
		out = append(out, web.SearchResult{
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

// LookupLocations forwards to the View, returning the per-tree
// placement of an artifact.
func (b *webBackingFS) LookupLocations(ctx context.Context, id domain.ArtifactID) (web.Locations, bool, error) {
	locs, ok := b.wfs.VFS().View().LookupLocations(id)
	if !ok {
		return web.Locations{}, false, nil
	}
	return web.Locations{
		ByArtifact:  locs.ByArtifact,
		BySession:   locs.BySession,
		ByNamespace: locs.ByNamespace,
		ByDate:      locs.ByDate,
		ByPath:      locs.ByPath,
		ByOrphaned:  locs.ByOrphaned,
	}, true, nil
}
