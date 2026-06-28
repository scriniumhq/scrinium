package vfs

import (
	"fmt"
	"sort"
	"strings"

	"scrinium.dev/projection/internal/view"
)

// statsBody returns the bytes served at _scrinium/stats. When a host
// installs a StatsProvider (the ScriniumClient wires one that renders
// the full report — daemon info, storage capacity, custom indexes), it
// owns the rendering. Absent a provider, the VFS falls back to a
// minimal dump of its own View counters: enough to be useful, but
// deliberately free of any cross-layer stats model so the facade
// stays a leaf over the projection primitive.
func (v *VFS) statsBody() []byte {
	if v.statsProvider != nil {
		return v.statsProvider()
	}
	s := v.view.StatsSnapshot()
	var b strings.Builder
	b.WriteString("Scrinium projection stats\n\n[view]\n")
	fmt.Fprintf(&b, "TotalNodes:       %d\n", s.TotalNodes)
	fmt.Fprintf(&b, "TotalBytes:       %d\n", s.TotalBytes)
	fmt.Fprintf(&b, "SessionCount:     %d\n", s.SessionCount)
	vcRoots := make([]string, 0, len(s.ViewCounts))
	for r := range s.ViewCounts {
		vcRoots = append(vcRoots, string(r))
	}
	sort.Strings(vcRoots)
	for _, r := range vcRoots {
		fmt.Fprintf(&b, "ViewCount[%s]:  %d\n", r, s.ViewCounts[view.RootView(r)])
	}
	fmt.Fprintf(&b, "OrphanedCount:    %d\n", s.OrphanedCount)
	fmt.Fprintf(&b, "CollisionCount:   %d\n", s.CollisionCount)
	fmt.Fprintf(&b, "TransitCount:     %d\n", s.TransitCount)
	return []byte(b.String())
}
