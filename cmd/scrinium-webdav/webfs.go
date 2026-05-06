package main

import (
	"context"
	"os"

	"github.com/rkurbatov/scrinium/cmd/scrinium-webdav/web"
)

// webBackingFS adapts the daemon's webdavFS to the web package's
// BackingFS interface. It's a paper-thin wrapper — the only real
// translation is OpenFile's return type, which web wants as
// web.File (a narrower interface than webdav.File). Go's
// structural subtyping does the conversion automatically once
// we say so explicitly.
type webBackingFS struct {
	wfs *webdavFS
}

func newWebBackingFS(wfs *webdavFS) *webBackingFS {
	return &webBackingFS{wfs: wfs}
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
