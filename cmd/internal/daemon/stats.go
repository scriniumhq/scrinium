// Package daemon holds plumbing shared by the scrinium-fuse,
// scrinium-webdav, and scrinium-webview entrypoints: composition glue
// that belongs neither on the ScriniumClient (the embeddable library
// API) nor in any primitive. It is importable only within cmd/.
package daemon

import (
	"context"
	"time"

	scrinium "scrinium.dev"
	"scrinium.dev/cmd/internal/stats"
	"scrinium.dev/domain"
)

// StatsProvider returns the closure a daemon installs via
// vfs.WithStatsProvider to back the _scrinium/stats pseudo-file. It
// joins the projection's counters (View.Stats), the store's physical
// capacity, and the daemon's runtime metadata, then renders the
// canonical text report. capacityTimeout caps the Store.Capacity call
// so a slow driver can't hang a stats read; on error capacity is
// omitted from the report.
//
// All three daemons rendered an identical report, so the glue lives
// here once rather than being copied per-main — and off the client,
// since rendering the stats surface is a daemon concern, not part of
// the library API. The projection returns stats in general form
// (ViewStats); the daemon composes and renders.
func StatsProvider(c *scrinium.ScriniumClient, startedAt time.Time, capacityTimeout time.Duration) func() []byte {
	return func() []byte {
		capCtx, cancel := context.WithTimeout(context.Background(), capacityTimeout)
		defer cancel()

		var capPtr *domain.StorageInfo
		if info, err := c.Store.Capacity(capCtx); err == nil {
			capPtr = &info
		}

		cis := make([]stats.CustomIndex, 0)
		for _, e := range c.CustomIndexes() {
			cis = append(cis, stats.CustomIndex{Name: e.Name, SchemaVersion: e.SchemaVersion})
		}

		meta := c.Info
		return stats.Render(c.Projection.View.Stats, stats.DaemonInfo{
			Source:        string(c.Projection.View.Source),
			StartedAt:     startedAt,
			MountSession:  c.MountSession,
			StorePath:     meta.StoreURI,
			ReadOnly:      meta.ReadOnly,
			Editing:       meta.Editing,
			Namespace:     meta.Namespace,
			Capacity:      capPtr,
			CustomIndexes: cis,
		})
	}
}
