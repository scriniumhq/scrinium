package projection

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/internal/humanize"
)

// DaemonInfo carries the per-process state RenderStats can't
// derive from the View alone. Daemons (FUSE, WebDAV) construct
// it once at startup and pass it on every render — RenderStats
// is stateless beyond what the View itself holds.
//
// Fields with no meaningful value should be left zero;
// RenderStats hides empty groups and "n/a"-renders absent
// numbers (Capacity == nil, Extensions == nil).
type DaemonInfo struct {
	// StartedAt is the daemon's startup timestamp. Used for
	// the Started/Uptime lines in the rendered output.
	StartedAt time.Time

	// MountSession is the per-process session id assigned to
	// every artifact this daemon writes. Useful when
	// inspecting "what did this mount produce so far".
	MountSession domain.SessionID

	// StorePath is the on-disk root the daemon was launched
	// against. Helps when multiple daemons run on one host.
	StorePath string

	// ReadOnly reflects the daemon's write policy. WebDAV and
	// FUSE both have a read-only mode — surface it so the
	// inspector knows whether writes were even possible.
	ReadOnly bool

	// Editing is the editing-mode label ("off" / "on" /
	// "custom") for daemons that have one. Empty hides the row.
	Editing string

	// Namespace is the default namespace stamped on artifacts
	// created through this daemon. Empty hides the row.
	Namespace string

	// Capacity is the optional storage snapshot from
	// store.Store.Capacity. nil hides the [storage] section
	// entirely. -1 inside fields means "Driver did not report".
	Capacity *domain.StorageInfo

	// Extensions lists the host-side index extensions
	// registered against the StoreIndex. nil hides the
	// [extensions] section. Order doesn't matter — RenderStats
	// sorts by Name for stable output.
	Extensions []ExtensionInfo
}

// ExtensionInfo is the projection-layer DTO for rendering
// information about a registered index extension. It is a
// pure render-time type — no behaviour, no dependencies on
// engine/index — so the projection package stays a leaf in
// the import graph.
//
// Callers that hold an index.ExtensionInfo (the contract type)
// translate field-for-field at the call site: the shapes are
// intentionally identical.
type ExtensionInfo struct {
	Name          string
	SchemaVersion int
}

// RenderStats produces the canonical text rendering used by
// FUSE's _scrinium/stats virtual file and WebDAV's same-named
// endpoint. Sections are grouped and labelled; empty groups are
// omitted; numbers that aren't available are rendered "n/a"
// rather than "-1" so the output stays readable.
//
// Format is plain text with one "key: value" per line. Stable
// across versions: tooling that scrapes it stays valid as long
// as the field names don't move.
func RenderStats(view *View, info DaemonInfo) []byte {
	var b strings.Builder
	b.WriteString("Scrinium projection stats\n\n")

	writeDaemonSection(&b, view, info)
	writeViewSection(&b, view)
	if info.Capacity != nil {
		writeStorageSection(&b, info.Capacity)
	}
	if info.Extensions != nil {
		writeExtensionsSection(&b, info.Extensions)
	}
	writeConfigSection(&b, info)

	return []byte(b.String())
}

// writeDaemonSection covers source kind, started/uptime, mount
// session, store path. Mount-session and store-path are skipped
// when the daemon left them empty (e.g. a future test harness
// that has no on-disk store).
func writeDaemonSection(b *strings.Builder, view *View, info DaemonInfo) {
	fmt.Fprintln(b, "[daemon]")
	fmt.Fprintf(b, "Source:           %s\n", view.Source)
	if !info.StartedAt.IsZero() {
		started := info.StartedAt.UTC().Format(time.RFC3339)
		fmt.Fprintf(b, "Started:          %s\n", started)
		fmt.Fprintf(b, "Uptime:           %s\n", formatUptime(time.Since(info.StartedAt)))
	}
	if info.MountSession != "" {
		fmt.Fprintf(b, "MountSession:     %s\n", info.MountSession)
	}
	if info.StorePath != "" {
		fmt.Fprintf(b, "StorePath:        %s\n", info.StorePath)
	}
	b.WriteString("\n")
}

// writeViewSection covers ViewStats. Always emitted — the View
// is the reason this endpoint exists.
func writeViewSection(b *strings.Builder, view *View) {
	stats := view.Stats
	fmt.Fprintln(b, "[view]")
	fmt.Fprintf(b, "TotalNodes:       %d\n", stats.TotalNodes)
	fmt.Fprintf(b, "TotalBytes:       %d (%s)\n", stats.TotalBytes, humanize.Bytes(stats.TotalBytes))
	fmt.Fprintf(b, "SessionCount:     %d\n", stats.SessionCount)
	fmt.Fprintf(b, "NamespaceCount:   %d\n", stats.NamespaceCount)
	fmt.Fprintf(b, "OrphanedCount:    %d\n", stats.OrphanedCount)
	fmt.Fprintf(b, "CollisionCount:   %d\n", stats.CollisionCount)
	fmt.Fprintf(b, "TransitCount:     %d\n", stats.TransitCount)
	if len(stats.ByStore) > 0 {
		// Sort by store name for deterministic output. The
		// names are short identifiers, lexicographic order is
		// fine.
		names := make([]string, 0, len(stats.ByStore))
		for name := range stats.ByStore {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			fmt.Fprintf(b, "ByStore[%s]:%s%d\n",
				name,
				strings.Repeat(" ", maxColWidth(name)),
				stats.ByStore[name])
		}
	}
	b.WriteString("\n")
}

// writeStorageSection covers StorageInfo from store.Store.Capacity.
// -1 in any field is rendered "n/a" (Driver didn't report). The
// difference between ArtifactCount and BlobCount surfaces
// deduplication efficiency at a glance.
func writeStorageSection(b *strings.Builder, c *domain.StorageInfo) {
	fmt.Fprintln(b, "[storage]")
	fmt.Fprintf(b, "ArtifactCount:    %s\n", formatCount(c.ArtifactCount))
	fmt.Fprintf(b, "BlobCount:        %s\n", formatCount(c.BlobCount))
	if c.ArtifactCount > 0 && c.BlobCount > 0 {
		ratio := float64(c.ArtifactCount) / float64(c.BlobCount)
		fmt.Fprintf(b, "DedupRatio:       %.2fx\n", ratio)
	}
	fmt.Fprintf(b, "TotalBytes:       %s\n", formatBytes(c.TotalBytes))
	fmt.Fprintf(b, "UsedBytes:        %s\n", formatBytes(c.UsedBytes))
	fmt.Fprintf(b, "AvailableBytes:   %s\n", formatBytes(c.AvailableBytes))
	b.WriteString("\n")
}

// writeExtensionsSection lists registered index extensions. Order
// is determined by Name to keep diff-friendly output across
// reads.
func writeExtensionsSection(b *strings.Builder, exts []ExtensionInfo) {
	fmt.Fprintln(b, "[extensions]")
	if len(exts) == 0 {
		fmt.Fprintln(b, "(none registered)")
		b.WriteString("\n")
		return
	}
	sorted := append([]ExtensionInfo(nil), exts...)
	slices.SortFunc(sorted, func(a, b ExtensionInfo) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, ext := range sorted {
		fmt.Fprintf(b, "%-30s v%d\n", ext.Name, ext.SchemaVersion)
	}
	b.WriteString("\n")
}

// writeConfigSection covers the daemon's policy switches that
// matter for diagnostics. Empty values are omitted so a daemon
// that doesn't have a concept of "editing" doesn't show that row.
func writeConfigSection(b *strings.Builder, info DaemonInfo) {
	hasAny := info.ReadOnly || info.Editing != "" || info.Namespace != ""
	if !hasAny {
		return
	}
	fmt.Fprintln(b, "[config]")
	fmt.Fprintf(b, "ReadOnly:         %v\n", info.ReadOnly)
	if info.Editing != "" {
		fmt.Fprintf(b, "Editing:          %s\n", info.Editing)
	}
	if info.Namespace != "" {
		fmt.Fprintf(b, "Namespace:        %s\n", info.Namespace)
	}
}

// formatUptime renders a duration with a coarse-grained, human-
// readable layout: 5d2h, 3h17m, 42s. We chop sub-second precision
// — ops doesn't care that the daemon has been up 12 microseconds.
func formatUptime(d time.Duration) string {
	if d < 0 {
		// Should not happen (StartedAt is always in the past),
		// but be defensive.
		d = -d
	}
	d = d.Round(time.Second)

	days := int64(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int64(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	minutes := int64(d / time.Minute)
	d -= time.Duration(minutes) * time.Minute
	seconds := int64(d / time.Second)

	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh%dm", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// formatBytes is humanBytes for storage values, treating -1 as
// "n/a". Used in the [storage] section where Driver may report
// "unavailable" for cloud backends.
func formatBytes(n int64) string {
	if n < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d (%s)", n, humanize.Bytes(n))
}

// formatCount is the integer counterpart, treating -1 as "n/a".
func formatCount(n int64) string {
	if n < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d", n)
}

// maxColWidth pads ByStore[name] to align its values with the
// other view rows. The "TotalBytes:       " column starts at 18
// characters; this helper computes the slack.
func maxColWidth(name string) int {
	const target = 18 // matches the other [view] rows
	used := len("ByStore[]:") + len(name)
	if used >= target {
		return 1
	}
	return target - used
}
