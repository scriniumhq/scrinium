package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/internal/humanize"
	"github.com/rkurbatov/scrinium/internal/pathx"
)

// dirEntry is one row in the rendered listing. Computed up-front
// so the template doesn't have to call helpers per row. We keep
// the original os.FileInfo so the artifact-id extractor (which
// type-asserts the FileInfo) can run later.
type dirEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
	Info    os.FileInfo
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
			Info:    fi,
		})
	}
	return out, nil
}

// sortDirEntries reorders entries by the requested column. The
// "dirs first" rule is preserved: directories always cluster at
// the top; the sort column orders each cluster independently.
// This matches what file managers do — sorting by size shouldn't
// scatter folders into the middle of files.
//
// column: "name" | "size" | "modified". Anything else falls back
// to "name".
// order: "asc" | "desc"; anything else falls back to "asc".
func sortDirEntries(entries []dirEntry, column, order string) {
	desc := order == "desc"

	less := func(i, j int) bool {
		a, b := entries[i], entries[j]
		// Dirs always before files regardless of sort.
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		switch column {
		case "size":
			if a.Size != b.Size {
				if desc {
					return a.Size > b.Size
				}
				return a.Size < b.Size
			}
			// Tie-break by name for determinism.
			return a.Name < b.Name
		case "modified":
			if !a.ModTime.Equal(b.ModTime) {
				if desc {
					return a.ModTime.After(b.ModTime)
				}
				return a.ModTime.Before(b.ModTime)
			}
			return a.Name < b.Name
		default: // "name"
			if desc {
				return a.Name > b.Name
			}
			return a.Name < b.Name
		}
	}
	sort.Slice(entries, less)
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
	BrowsePrefix string

	// Totals summarise the directory's contents under the
	// table. Counts cover the entire directory; pagination
	// truncates the visible rows but the totals always
	// reflect the full set.
	TotalDirs     int
	TotalFiles    int
	TotalBytes    int64
	TotalBytesFmt string

	// Sort fields drive the column-header arrows and the
	// links each header generates. The active sort column
	// shows a "↑" or "↓" suffix; inactive columns link to
	// "?sort=<col>&order=asc".
	SortColumn    string // "name" | "size" | "modified"
	SortOrder     string // "asc" | "desc"
	SortNameURL   string
	SortSizeURL   string
	SortTimeURL   string
	SortNameArrow string
	SortSizeArrow string
	SortTimeArrow string

	// Pagination metadata. Empty/zero when the directory
	// fits in a single page; non-zero values activate the
	// "prev / next" controls in the footer.
	Page       int
	TotalPages int
	PrevURL    string
	NextURL    string
}

type crumb struct {
	Name string
	URL  string
}

type listingRow struct {
	Name     string
	URL      string // info-page link for files; subdir link for directories
	IsDir    bool
	IsSystem bool
	SizeText string
	TimeText string

	// ViewURL is the inline-render endpoint, populated only
	// when the file's MIME is in the conservative "browser
	// will render this" whitelist. Empty otherwise — the
	// template skips the [view] action when so.
	ViewURL string

	// DownloadURL is the attachment-disposition endpoint,
	// populated for every file regardless of MIME. Empty for
	// directories.
	DownloadURL string
}

// pageSize bounds how many rows the listing page renders at
// once. Big enough to fit a normal directory in a single
// scroll, small enough to keep heavy by-date trees from
// loading thousands of rows. ?page=N is 1-based.
const pageSize = 200

// serveListing renders a directory page.
func (h *Handler) serveListing(w http.ResponseWriter, r *http.Request, dir string) {
	entries, err := h.dirEntries(r.Context(), dir)
	if err != nil {
		h.serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("list %q: %v", dir, err))
		return
	}

	// Apply the requested sort before pagination — pagination
	// then carves a window out of an already-ordered list, so
	// page 2 starts where page 1 ended in the chosen order.
	q := r.URL.Query()
	sortCol := q.Get("sort")
	if sortCol != "size" && sortCol != "modified" {
		sortCol = "name"
	}
	sortOrder := q.Get("order")
	if sortOrder != "desc" {
		sortOrder = "asc"
	}
	sortDirEntries(entries, sortCol, sortOrder)

	// Compute totals over the full (unpaginated) entry set.
	// Pagination truncates the visible rows but the summary
	// always reflects the directory's full contents.
	var totalDirs, totalFiles int
	var totalBytes int64
	for _, e := range entries {
		if e.IsDir {
			totalDirs++
		} else {
			totalFiles++
			totalBytes += e.Size
		}
	}

	// Pagination. Empty page param → page 1.
	page := 1
	if p := q.Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	totalPages := (len(entries) + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > len(entries) {
		end = len(entries)
	}
	pageEntries := entries[start:end]

	data := listingData{
		Path:          "/" + dir,
		Crumbs:        h.buildCrumbs(dir),
		StorePath:     h.cfg.StorePath,
		NowFormatted:  time.Now().UTC().Format(time.RFC3339),
		StatsURL:      "/" + h.cfg.ServicePrefix + "/stats",
		BrowsePrefix:  h.prefix,
		TotalDirs:     totalDirs,
		TotalFiles:    totalFiles,
		TotalBytes:    totalBytes,
		TotalBytesFmt: humanize.Bytes(totalBytes),
		SortColumn:    sortCol,
		SortOrder:     sortOrder,
		Page:          page,
		TotalPages:    totalPages,
	}

	// Sort header URLs. Clicking the active column flips
	// asc↔desc; clicking another column starts at asc.
	data.SortNameURL = sortLinkURL(r.URL, "name", sortCol, sortOrder)
	data.SortSizeURL = sortLinkURL(r.URL, "size", sortCol, sortOrder)
	data.SortTimeURL = sortLinkURL(r.URL, "modified", sortCol, sortOrder)
	data.SortNameArrow = sortArrow("name", sortCol, sortOrder)
	data.SortSizeArrow = sortArrow("size", sortCol, sortOrder)
	data.SortTimeArrow = sortArrow("modified", sortCol, sortOrder)

	// Pagination URLs preserve sort params so navigating
	// pages doesn't reset the column order.
	if page > 1 {
		data.PrevURL = pageLinkURL(r.URL, page-1)
	}
	if page < totalPages {
		data.NextURL = pageLinkURL(r.URL, page+1)
	}

	if dir != "" && !isServiceTreeRoot(dir) {
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

	for _, e := range pageEntries {
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
		if e.IsDir {
			// Directories: clicking the name navigates
			// further down the tree, same as before.
			base := r.URL.Path
			if !strings.HasSuffix(base, "/") {
				base += "/"
			}
			row.URL = base + e.Name + "/"
			row.SizeText = "—"
		} else {
			// Files: clicking the name opens the artifact
			// info page — the web surface is diagnostic, so
			// "info" is the default action. Inline rendering
			// and downloading are explicit alternatives
			// reachable via the [view] / [dl] buttons.
			row.SizeText = humanize.Bytes(e.Size)
			id := extractArtifactID(e.Info)
			if id != "" {
				row.URL = h.prefix + "/_artifact/" + string(id)
				row.DownloadURL = h.prefix + "/_download/" + string(id)
				// MIME source priority: fsmeta first, then
				// the filename extension as fallback. Many
				// artifacts are written without an explicit
				// fsmeta MIME (the producer didn't bother)
				// but the filename still gives us enough
				// to advertise [view] for known types.
				mimeType := inferMIME(e.Name, extractMIME(e.Info))
				if isInlineable(mimeType) {
					row.ViewURL = h.prefix + "/_view/" + string(id)
				}
			}
			// Files without an ArtifactID (synthetic stats
			// file, etc.) leave URL empty — the template
			// renders the name as plain text.
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
	return pathx.IsUnder(p, sp)
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

// sortLinkURL builds the href for a sortable column header.
// Clicking the active column flips asc↔desc; clicking another
// column starts at asc. The URL preserves the current page so
// users don't lose their position when re-sorting (re-sorting
// usually invalidates the page anyway, but a stable behaviour
// avoids surprises).
//
// orig is the request URL we mutate; col is the column the
// header represents; activeCol/activeOrder describe the
// current sort.
func sortLinkURL(orig *url.URL, col, activeCol, activeOrder string) string {
	q := orig.Query()
	q.Set("sort", col)
	if col == activeCol && activeOrder == "asc" {
		q.Set("order", "desc")
	} else {
		q.Set("order", "asc")
	}
	out := *orig
	out.RawQuery = q.Encode()
	return out.RequestURI()
}

// sortArrow returns the arrow suffix for a column header
// matching the active sort. Empty string for inactive columns
// keeps the header clean.
func sortArrow(col, activeCol, activeOrder string) string {
	if col != activeCol {
		return ""
	}
	if activeOrder == "desc" {
		return " ↓"
	}
	return " ↑"
}

// pageLinkURL builds the href for prev/next pagination
// buttons. Preserves all other query params (sort, order)
// so navigation doesn't disturb sort.
func pageLinkURL(orig *url.URL, page int) string {
	q := orig.Query()
	if page <= 1 {
		q.Del("page")
	} else {
		q.Set("page", strconv.Itoa(page))
	}
	out := *orig
	out.RawQuery = q.Encode()
	return out.RequestURI()
}

// isServiceTreeRoot reports whether dir is exactly one of
// the service tree names (by-path, by-date, by-session,
// by-namespace, by-artifact, orphaned) — and not a sub-path
// inside one. Used to suppress the ".." entry on tree roots:
// climbing up from /by-date/ would land on the redirect-only
// "/" handler, which is confusing UX.
//
// Stays in web pkg rather than in projection because it's a
// presentation concern: the projection's Route function
// happily resolves these paths regardless.
func isServiceTreeRoot(dir string) bool {
	switch dir {
	case "by-path", "by-date", "by-session", "by-namespace",
		"by-artifact", "orphaned":
		return true
	}
	return false
}
