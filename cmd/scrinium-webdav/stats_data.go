package main

import (
	"fmt"
	"time"

	"github.com/rkurbatov/scrinium/cmd/scrinium-webdav/web"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/projection"
)

// buildWebStatsData translates the daemon's live state into the
// structured shape web.StatsData. Mirrors the logic in
// projection.RenderStats: same "n/a" sentinels, same dedup ratio
// math, same byte humanisation — just emitting structured data
// for the HTML template instead of writing text.
//
// Kept in main rather than under web/ so the web package stays
// schema-agnostic; this helper knows about projection-level
// types (View, ExtensionInfo) which web cannot import.
func buildWebStatsData(
	view *projection.View,
	cap *domain.StorageInfo,
	exts []web.StatsExtension,
	startedAt time.Time,
	mountSession string,
	cfg Config,
) web.StatsData {
	stats := view.Stats

	d := web.StatsData{
		Daemon: web.StatsDaemon{
			Source:       string(view.Source),
			StartedAt:    startedAt,
			Uptime:       formatStatsUptime(time.Since(startedAt)),
			MountSession: mountSession,
			StorePath:    cfg.Daemon.Store,
		},
		View: web.StatsView{
			TotalNodes:     stats.TotalNodes,
			TotalBytes:     stats.TotalBytes,
			TotalBytesText: fmt.Sprintf("%d (%s)", stats.TotalBytes, web.HumanSize(stats.TotalBytes)),
			SessionCount:   stats.SessionCount,
			NamespaceCount: stats.NamespaceCount,
			OrphanedCount:  stats.OrphanedCount,
			CollisionCount: stats.CollisionCount,
			TransitCount:   stats.TransitCount,
			ByStore:        stats.ByStore,
		},
		Extensions: exts,
	}

	if cap != nil {
		d.Storage = web.StatsStorage{
			ArtifactCount:  formatCountOrNA(cap.ArtifactCount),
			BlobCount:      formatCountOrNA(cap.BlobCount),
			DedupRatio:     formatDedup(cap.ArtifactCount, cap.BlobCount),
			TotalBytes:     formatBytesOrNA(cap.TotalBytes),
			UsedBytes:      formatBytesOrNA(cap.UsedBytes),
			AvailableBytes: formatBytesOrNA(cap.AvailableBytes),
		}
		d.HasStorage = true
	}

	if cfg.Daemon.ReadOnly || cfg.Daemon.Editing != "" || cfg.Daemon.Namespace != "" {
		d.Config = web.StatsConfig{
			ReadOnly:  cfg.Daemon.ReadOnly,
			Editing:   cfg.Daemon.Editing,
			Namespace: cfg.Daemon.Namespace,
		}
		d.HasConfig = true
	}

	return d
}

// formatCountOrNA renders an int64 count, treating -1 as "n/a"
// (the StorageInfo sentinel for "Driver did not report").
func formatCountOrNA(n int64) string {
	if n < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d", n)
}

// formatBytesOrNA renders bytes with a humanised parenthetical
// suffix, treating -1 as "n/a".
func formatBytesOrNA(n int64) string {
	if n < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d (%s)", n, web.HumanSize(n))
}

// formatDedup synthesises ArtifactCount / BlobCount as a ratio
// when both values are known and non-zero. Returns empty string
// otherwise — the template hides the row.
func formatDedup(artifacts, blobs int64) string {
	if artifacts <= 0 || blobs <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2fx", float64(artifacts)/float64(blobs))
}

// formatStatsUptime renders a duration with a coarse layout:
// "5d2h", "3h17m", "42s". Mirrors projection.formatUptime so
// the textual and HTML stats agree on uptime presentation.
func formatStatsUptime(d time.Duration) string {
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
