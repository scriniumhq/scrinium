package vfs

import (
	"io"
	"os"
	"time"

	"scrinium.dev/projection/internal/view"
)

// rootDirFile listings come from FSOps.Listdir(""). The
// service prefix entry is appended (when configured).
type rootDirFile struct {
	dirFileStub
	v        *VFS
	consumed bool
}

func newRootDirFile(v *VFS) *rootDirFile { return &rootDirFile{v: v} }

func (d *rootDirFile) Close() error { return nil }

func (d *rootDirFile) Readdir(count int) ([]os.FileInfo, error) {
	if d.consumed {
		return nil, io.EOF
	}
	d.consumed = true
	var out []os.FileInfo
	for fi, err := range d.v.fsops.Listdir("") {
		if err != nil {
			return nil, err
		}
		if d.v.nameFilter != nil && d.v.nameFilter(fi.Name) {
			continue
		}
		out = append(out, projectionFileInfo{fi: fi})
	}
	if d.v.routingCfg.ServicePrefix != "" {
		out = append(out, synthDirInfo(d.v.routingCfg.ServicePrefix, d.v.startedAt))
	}
	return out, nil
}

func (d *rootDirFile) Stat() (os.FileInfo, error) {
	return synthDirInfo("/", d.v.startedAt), nil
}

// pathDirFile is a directory inside the root view. Delegates
// listing to FSOps.
type pathDirFile struct {
	dirFileStub
	v        *VFS
	subPath  string
	consumed bool
}

func newPathDirFile(v *VFS, subPath string) *pathDirFile {
	return &pathDirFile{v: v, subPath: subPath}
}

func (d *pathDirFile) Close() error { return nil }

func (d *pathDirFile) Readdir(count int) ([]os.FileInfo, error) {
	if d.consumed {
		return nil, io.EOF
	}
	d.consumed = true
	var out []os.FileInfo
	for fi, err := range d.v.fsops.Listdir(d.subPath) {
		if err != nil {
			return nil, err
		}
		if d.v.nameFilter != nil && d.v.nameFilter(fi.Name) {
			continue
		}
		out = append(out, projectionFileInfo{fi: fi})
	}
	return out, nil
}

func (d *pathDirFile) Stat() (os.FileInfo, error) {
	fi, err := d.v.fsops.Stat(d.subPath)
	if err != nil {
		return nil, err
	}
	return projectionFileInfo{fi: fi}, nil
}

// serviceDirFile lists service-tree directories or the
// service prefix root. Two modes:
//
//   - serviceRoot: the prefix dir itself (lists trees + stats).
//   - serviceTree: a directory inside a tree (lists View nodes).
//
// Service-tree listings ignore the nameFilter — these are
// diagnostic surfaces; full visibility is the contract.
type serviceDirFile struct {
	dirFileStub
	v        *VFS
	tree     view.RootView
	subPath  string
	isPrefix bool
	consumed bool
}

func newServiceDirFile(v *VFS, treeOrPrefix any, subPath string, isPrefix bool) *serviceDirFile {
	d := &serviceDirFile{
		v:        v,
		subPath:  subPath,
		isPrefix: isPrefix,
	}
	if !isPrefix {
		if t, ok := treeOrPrefix.(view.RootView); ok {
			d.tree = t
		}
	}
	return d
}

func (d *serviceDirFile) Close() error { return nil }

func (d *serviceDirFile) Readdir(count int) ([]os.FileInfo, error) {
	if d.consumed {
		return nil, io.EOF
	}
	d.consumed = true
	if d.isPrefix {
		// List enabled service trees + stats.
		cfg := d.v.routingCfg
		var out []os.FileInfo
		add := func(name string) {
			out = append(out, synthDirInfo(name, d.v.startedAt))
		}
		if cfg.ShowStats {
			out = append(out, synthFileInfo("stats", int64(len(d.v.statsBody())), time.Now()))
		}
		if cfg.ShowByArtifact {
			add("by-artifact")
		}
		if cfg.ShowByDate {
			add("by-date")
		}
		if cfg.ShowBySession {
			add("by-session")
		}
		if cfg.ShowOrphaned {
			add("orphaned")
		}
		if cfg.ShowRaw {
			add("raw")
		}
		// Extension-contributed roots (by-path, by-namespace, …): the
		// surface names none of them; it lists whatever the View reports.
		if cfg.ShowProvidedViews {
			for _, r := range d.v.view.ProvidedRoots() {
				add(string(r))
			}
		}
		return out, nil
	}
	// Service-tree listing.
	var out []os.FileInfo
	for n, err := range d.v.view.ListIn(d.tree, d.subPath) {
		if err != nil {
			return nil, err
		}
		out = append(out, projectionNodeInfo{node: n, fallbackTime: d.v.startedAt})
	}
	return out, nil
}

func (d *serviceDirFile) Stat() (os.FileInfo, error) {
	if d.isPrefix {
		return synthDirInfo(d.v.routingCfg.ServicePrefix, d.v.startedAt), nil
	}
	node, err := d.v.view.GetIn(d.tree, d.subPath)
	if err != nil {
		return nil, err
	}
	return projectionNodeInfo{node: node, fallbackTime: d.v.startedAt}, nil
}

// Compile-time guards.
var (
	_ File = (*rootDirFile)(nil)
	_ File = (*pathDirFile)(nil)
	_ File = (*serviceDirFile)(nil)
)
