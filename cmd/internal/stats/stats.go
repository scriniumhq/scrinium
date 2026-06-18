package stats

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"scrinium.dev/cmd/internal/humanize"
	"scrinium.dev/projection"

	"scrinium.dev/domain"
)

// Extension is the render-time DTO for a loaded extension. Callers
// holding an extension.Descriptor translate field-for-field.
type Extension struct {
	Name string
}

// DaemonInfo carries the per-process and cross-layer state the report
// can't derive from the projection counters alone. Fields with no
// meaningful value are left zero; Render hides empty groups and
// "n/a"-renders absent numbers (Capacity == nil, Extensions == nil).
type DaemonInfo struct {
	// Source labels the projection's backing source kind (e.g.
	// the source.Kind the View was built from), rendered verbatim.
	Source string

	// StartedAt is the daemon's startup timestamp, driving the
	// Started/Uptime lines.
	StartedAt time.Time

	// MountSession is the per-process session id stamped on every
	// artifact this daemon writes.
	MountSession domain.SessionID

	// StorePath is the on-disk root the daemon was launched against.
	StorePath string

	// ReadOnly reflects the daemon's write policy.
	ReadOnly bool

	// Editing is the editing-mode label ("off"/"on"/"custom").
	// Empty hides the row.
	Editing string

	// Capacity is the optional storage snapshot from
	// store.Store.Capacity. nil hides the [storage] section; -1 in
	// a field means "driver did not report".
	Capacity *domain.StorageInfo

	// Extensions lists loaded extensions. nil hides the [extensions]
	// section; order is normalised (sorted by Name).
	Extensions []Extension
}

// Render produces the canonical text report. Sections are grouped and
// labelled; empty groups are omitted; unavailable numbers render
// "n/a" rather than "-1". Format is plain "key: value" lines, stable
// across versions as long as field names don't move.
func Render(vs projection.Stats, info DaemonInfo) []byte {
	var b strings.Builder
	b.WriteString("Scrinium projection stats\n\n")

	writeDaemonSection(&b, info)
	writeViewSection(&b, vs)
	if info.Capacity != nil {
		writeStorageSection(&b, info.Capacity)
	}
	if info.Extensions != nil {
		writeExtensionsSection(&b, info.Extensions)
	}
	writeConfigSection(&b, info)

	return []byte(b.String())
}

func writeDaemonSection(b *strings.Builder, info DaemonInfo) {
	fmt.Fprintln(b, "[daemon]")
	fmt.Fprintf(b, "Source:           %s\n", info.Source)
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

func writeViewSection(b *strings.Builder, stats projection.Stats) {
	fmt.Fprintln(b, "[view]")
	fmt.Fprintf(b, "TotalNodes:       %d\n", stats.TotalNodes)
	fmt.Fprintf(b, "TotalBytes:       %s\n", humanize.BytesWithRaw(stats.TotalBytes))
	fmt.Fprintf(b, "SessionCount:     %d\n", stats.SessionCount)
	vcRoots := make([]string, 0, len(stats.ViewCounts))
	for r := range stats.ViewCounts {
		vcRoots = append(vcRoots, string(r))
	}
	slices.Sort(vcRoots)
	for _, r := range vcRoots {
		fmt.Fprintf(b, "ViewCount[%s]:  %d\n", r, stats.ViewCounts[projection.RootView(r)])
	}
	fmt.Fprintf(b, "OrphanedCount:    %d\n", stats.OrphanedCount)
	fmt.Fprintf(b, "CollisionCount:   %d\n", stats.CollisionCount)
	fmt.Fprintf(b, "TransitCount:     %d\n", stats.TransitCount)
	if len(stats.ByStore) > 0 {
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

func writeStorageSection(b *strings.Builder, c *domain.StorageInfo) {
	fmt.Fprintln(b, "[storage]")
	fmt.Fprintf(b, "ArtifactCount:    %s\n", formatCount(c.ArtifactCount))
	fmt.Fprintf(b, "BlobCount:        %s\n", formatCount(c.BlobCount))
	if c.ArtifactCount > 0 && c.BlobCount > 0 {
		ratio := float64(c.ArtifactCount) / float64(c.BlobCount)
		fmt.Fprintf(b, "DedupRatio:       %.2fx\n", ratio)
	}
	fmt.Fprintf(b, "TotalBytes:       %s\n", humanize.BytesOrNA(c.TotalBytes))
	fmt.Fprintf(b, "UsedBytes:        %s\n", humanize.BytesOrNA(c.UsedBytes))
	fmt.Fprintf(b, "AvailableBytes:   %s\n", humanize.BytesOrNA(c.AvailableBytes))
	b.WriteString("\n")
}

func writeExtensionsSection(b *strings.Builder, exts []Extension) {
	fmt.Fprintln(b, "[extensions]")
	if len(exts) == 0 {
		fmt.Fprintln(b, "(none registered)")
		b.WriteString("\n")
		return
	}
	sorted := append([]Extension(nil), exts...)
	slices.SortFunc(sorted, func(a, b Extension) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, ext := range sorted {
		fmt.Fprintln(b, ext.Name)
	}
	b.WriteString("\n")
}

func writeConfigSection(b *strings.Builder, info DaemonInfo) {
	hasAny := info.ReadOnly || info.Editing != ""
	if !hasAny {
		return
	}
	fmt.Fprintln(b, "[config]")
	fmt.Fprintf(b, "ReadOnly:         %v\n", info.ReadOnly)
	if info.Editing != "" {
		fmt.Fprintf(b, "Editing:          %s\n", info.Editing)
	}
}

// formatUptime renders a duration coarsely: 5d2h0m, 3h17m, 42s.
func formatUptime(d time.Duration) string {
	if d < 0 {
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

func formatCount(n int64) string {
	if n < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d", n)
}

// maxColWidth pads ByStore[name] so values align with the other
// [view] rows (column starts at 18 chars).
func maxColWidth(name string) int {
	const target = 18
	used := len("ByStore[]:") + len(name)
	if used >= target {
		return 1
	}
	return target - used
}
