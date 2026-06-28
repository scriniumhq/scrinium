package web

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// searchPageData binds the search HTML template.
type searchPageData struct {
	Layout

	// Query echoes the user's input back into the form.
	Query string

	// Results is the list of hits, capped at searchLimit. Empty
	// when the query yielded nothing (or before any query was
	// submitted — that case the template differentiates via
	// HasQuery).
	Results []searchResultRow

	// HasQuery distinguishes "you haven't searched yet" (show
	// help text) from "you searched and got zero hits" (show
	// no-results message). Both produce empty Results.
	HasQuery bool

	// Truncated signals that more hits exist beyond the cap;
	// the template surfaces a note prompting the user to refine.
	Truncated bool
	Limit     int
}

// searchResultRow is one row, with display-ready strings.
type searchResultRow struct {
	URL         string
	Path        string // empty → "(orphaned)"
	SessionID   string
	CreatedAt   string
	MIME        string
	MatchReason string
	IsOrphan    bool
}

// searchLimit caps how many results we return per query.
// Higher than typical screens hold so the user sees enough
// context, low enough to keep the page snappy on big stores.
// The "Truncated" flag tells the user when they've hit the cap.
const searchLimit = 200

// serveSearch renders the search page. Empty ?q= shows just
// the form with help text; non-empty triggers the search and
// renders results.
func (h *Handler) serveSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	data := searchPageData{
		Layout:   h.layout(),
		Query:    q,
		HasQuery: q != "",
		Limit:    searchLimit,
	}

	if q != "" {
		results, err := h.fs.Search(r.Context(), q, searchLimit+1)
		if err == nil {
			// Detect truncation: ask for one more than the cap;
			// if we got that many, we know more exist.
			if len(results) > searchLimit {
				data.Truncated = true
				results = results[:searchLimit]
			}
			for _, sr := range results {
				row := searchResultRow{
					URL:         h.prefix + "/_artifact/" + string(sr.ArtifactID),
					Path:        sr.Path,
					SessionID:   string(sr.SessionID),
					CreatedAt:   sr.CreatedAt.UTC().Format(time.RFC3339),
					MIME:        sr.MIME,
					MatchReason: sr.MatchReason,
					IsOrphan:    sr.Path == "",
				}
				data.Results = append(data.Results, row)
			}
		} else {
			fmt.Fprintf(os.Stderr, "scrinium-web: search %q: %v\n", q, err)
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	if err := render(w, "search", data); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-web: search render: %v\n", err)
	}
}
