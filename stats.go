package scrinium

import (
	"context"
	"time"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/projection"
)

// StatsProvider returns a function that renders the runtime's
// stats snapshot as bytes — the format consumed by the
// _scrinium/stats endpoint in every reference surface.
//
// The returned closure captures startedAt (the time the
// surface began serving) and reaches into Scrinium for the
// rest. Surfaces wire it into their stats route or pseudo-file
// handler.
//
// The capacityTimeout caps the call to Store.Capacity — a slow
// driver should never hang `cat _scrinium/stats`. Pass a
// reasonable value like 2*time.Second; on timeout or other
// error capacity is omitted from the output (rendered as "n/a"
// fields rather than failing the whole stats read).
//
// Use this helper instead of building the snapshot manually —
// it tracks the format projection.RenderStats expects, and
// surfaces inherit improvements (new fields, format tweaks)
// without each having to update.
func (s *Scrinium) StatsProvider(ctx context.Context, startedAt time.Time, capacityTimeout time.Duration) func() []byte {
	return func() []byte {
		capCtx, capCancel := context.WithTimeout(ctx, capacityTimeout)
		defer capCancel()

		var capPtr *domain.StorageInfo
		if cap, err := s.Store.Capacity(capCtx); err == nil {
			capPtr = &cap
		}

		exts := make([]projection.ExtensionInfo, 0)
		for _, e := range s.ListExtensions() {
			exts = append(exts, projection.ExtensionInfo{
				Name:          e.Name,
				SchemaVersion: e.SchemaVersion,
			})
		}

		return projection.RenderStats(s.View, projection.DaemonInfo{
			StartedAt:    startedAt,
			MountSession: s.MountSession,
			StorePath:    s.Config.Store,
			ReadOnly:     s.Config.ReadOnly,
			Editing:      s.Config.Editing,
			Namespace:    s.Config.Namespace,
			Capacity:     capPtr,
			Extensions:   exts,
		})
	}
}
