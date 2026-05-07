package vfs

import "github.com/rkurbatov/scrinium/projection"

// statsBody returns the bytes served at _scrinium/stats. If
// a host installed a StatsProvider, it owns the rendering
// (typically projection.RenderStats with full DaemonInfo);
// otherwise we fall back to a minimal View-only render so
// surfaces without a provider still see meaningful output.
func (v *VFS) statsBody() []byte {
	if v.statsProvider != nil {
		return v.statsProvider()
	}
	return projection.RenderStats(v.view, projection.DaemonInfo{
		StartedAt: v.startedAt,
	})
}
