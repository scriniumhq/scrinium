// Package surfacekit holds helpers shared by the reference surfaces
// (webdav, fuse, webview): rendering the stats snapshot from a runtime,
// the routing/visibility config block surfaces embed, and decoding a
// surface's generic config map into a typed struct.
//
// It is internal to engine/surface so it stays an implementation
// detail of the bundled surfaces, not a public contract.
package surfacekit

import (
	"context"
	"time"

	"gopkg.in/yaml.v3"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/projection"
	"scrinium.dev/engine/runtime"
)

// Routing is the service-tree visibility block a surface embeds in its
// own config (`yaml:",inline"`). Each surface picks its own defaults —
// WebDAV turns every tree off, FUSE turns them on — which is the
// per-surface override the hybrid projection model allows.
type Routing struct {
	ServicePrefix   string `yaml:"servicePrefix"`
	RootView        string `yaml:"rootView"`
	ShowStats       bool   `yaml:"showStats"`
	ShowByArtifact  bool   `yaml:"showByArtifact"`
	ShowOrphaned    bool   `yaml:"showOrphaned"`
	ShowByDate      bool   `yaml:"showByDate"`
	ShowBySession   bool   `yaml:"showBySession"`
	ShowByNamespace bool   `yaml:"showByNamespace"`
	ShowRaw         bool   `yaml:"showRaw"`
}

// Config converts the block into the projection routing snapshot the
// View and surface routers consume.
func (r Routing) Config() projection.RoutingConfig {
	return projection.RoutingConfig{
		ServicePrefix:   r.ServicePrefix,
		RootView:        projection.RootView(r.RootView),
		ShowStats:       r.ShowStats,
		ShowByArtifact:  r.ShowByArtifact,
		ShowOrphaned:    r.ShowOrphaned,
		ShowByDate:      r.ShowByDate,
		ShowBySession:   r.ShowBySession,
		ShowByNamespace: r.ShowByNamespace,
		ShowRaw:         r.ShowRaw,
	}
}

// StatsProvider returns a closure that renders the runtime's stats
// snapshot as bytes for the _scrinium/stats pseudo-file. capacityTimeout
// caps Store.Capacity so a slow driver never hangs a stats read; on
// error capacity is omitted from the output.
func StatsProvider(rt runtime.Runtime, startedAt time.Time, capacityTimeout time.Duration) func() []byte {
	return func() []byte {
		capCtx, cancel := context.WithTimeout(context.Background(), capacityTimeout)
		defer cancel()

		var capPtr *domain.StorageInfo
		if info, err := rt.Store().Capacity(capCtx); err == nil {
			capPtr = &info
		}

		exts := make([]projection.ExtensionInfo, 0)
		if lister, ok := rt.Index().(index.ExtensionLister); ok {
			for _, e := range lister.ListExtensions() {
				exts = append(exts, projection.ExtensionInfo{
					Name:          e.Name,
					SchemaVersion: e.SchemaVersion,
				})
			}
		}

		meta := rt.Info()
		return projection.RenderStats(rt.View(), projection.DaemonInfo{
			StartedAt:    startedAt,
			MountSession: rt.MountSession(),
			StorePath:    meta.StoreURI,
			ReadOnly:     meta.ReadOnly,
			Editing:      meta.Editing,
			Namespace:    meta.Namespace,
			Capacity:     capPtr,
			Extensions:   exts,
		})
	}
}

// DecodeConfig fills the typed struct from a surface's generic config
// map via a YAML round-trip. Defaults already set on into are kept for
// keys the map omits.
func DecodeConfig(raw map[string]any, into any) error {
	if len(raw) == 0 {
		return nil
	}
	b, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, into)
}
