package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"time"
)

// dirEntry is one row in the rendered listing. Computed up-front
// so the template doesn't have to call helpers per row.
type dirEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// dirEntries enumerates a directory through the BackingFS.
// Returns entries sorted directories-first, then alphabetically.
func (h *Handler) dirEntries(ctx context.Context, dir string) ([]dirEntry, error) {
	f, err := h.fs.OpenFile(ctx, dir, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	out := make([]dirEntry, 0, len(infos))
	for _, fi := range infos {
		out = append(out, dirEntry{
			Name:    fi.Name(),
			IsDir:   fi.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
		})
	}
	// Stable order: directories first, then files, both sorted
	// by name. Browsers don't reorder, so we have to.
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// listingData binds the HTML template. Field names match
// template variables; rename means template edit.
type listingData struct {
	Path         string
	Crumbs       []crumb
	Parent       string
	HasParent    bool
	Entries      []listingRow
	StorePath    string
	NowFormatted string
	StatsURL     string
}

type crumb struct {
	Name string
	URL  string
}

type listingRow struct {
	Name     string
	URL      string
	IsDir    bool
	IsSystem bool
	SizeText string
	TimeText string
}

// serveListing renders a directory page.
func (h *Handler) serveListing(w http.ResponseWriter, r *http.Request, dir string) {
	entries, err := h.dirEntries(r.Context(), dir)
	if err != nil {
		h.serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("list %q: %v", dir, err))
		return
	}

	data := listingData{
		Path:         "/" + dir,
		Crumbs:       h.buildCrumbs(dir),
		StorePath:    h.cfg.StorePath,
		NowFormatted: time.Now().UTC().Format(time.RFC3339),
		StatsURL:     "/" + h.cfg.ServicePrefix + "/stats",
	}
	if dir != "" {
		parent := pathpkg.Dir(dir)
		if parent == "." {
			parent = ""
		}
		if parent == "" {
			data.Parent = h.prefix + "/"
		} else {
			data.Parent = h.prefix + "/" + parent + "/"
		}
		data.HasParent = true
	}

	for _, e := range entries {
		// Full path of the entry (relative to view root).
		// dir == "" for the root listing; build the entry's
		// path so we can decide whether it falls under the
		// service prefix without ambiguity.
		entryPath := e.Name
		if dir != "" {
			entryPath = dir + "/" + e.Name
		}
		row := listingRow{
			Name:     e.Name,
			IsDir:    e.IsDir,
			IsSystem: h.isSystemPath(entryPath),
			TimeText: e.ModTime.UTC().Format("2006-01-02 15:04"),
		}
		base := r.URL.Path
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		row.URL = base + e.Name
		if e.IsDir {
			row.URL += "/"
			row.SizeText = "—"
		} else {
			row.SizeText = HumanSize(e.Size)
		}
		data.Entries = append(data.Entries, row)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := listingTemplate.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-web: render listing: %v\n", err)
	}
}

// isSystemPath reports whether the given store-relative path
// falls under the daemon's service prefix. Only entries inside
// (or matching) ServicePrefix are system; user-created files
// or directories with a leading underscore in any other
// location remain regular content.
//
// Comparison is exact: the path must equal the prefix or begin
// with prefix + "/". An empty service prefix disables the
// classification entirely (everything is treated as user
// content), which matches the daemon's contract — empty prefix
// means no service tree exists.
func (h *Handler) isSystemPath(p string) bool {
	sp := h.cfg.ServicePrefix
	if sp == "" {
		return false
	}
	if p == sp {
		return true
	}
	return strings.HasPrefix(p, sp+"/")
}

// buildCrumbs renders the path bar above the listing.
func (h *Handler) buildCrumbs(dir string) []crumb {
	out := []crumb{{Name: "root", URL: h.prefix + "/"}}
	if dir == "" {
		return out
	}
	parts := strings.Split(dir, "/")
	acc := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if acc == "" {
			acc = p
		} else {
			acc = acc + "/" + p
		}
		out = append(out, crumb{
			Name: p,
			URL:  h.prefix + "/" + acc + "/",
		})
	}
	return out
}
