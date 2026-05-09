package scrinium

import "github.com/rkurbatov/scrinium/projection"

// RoutingConfig is a snapshot of the visibility/policy fields
// from the runtime's Config in the shape projection.View and
// the surface routers consume. Surfaces typically build it
// once at startup and pass it down to their tree builders.
//
// Use this helper instead of constructing projection.RoutingConfig
// manually — it tracks Scrinium's preferred defaults and means a
// new Show* flag added later only needs to be wired here.
func (s *Scrinium) RoutingConfig() projection.RoutingConfig {
	c := s.Config
	return projection.RoutingConfig{
		ServicePrefix:   c.ServicePrefix,
		RootView:        c.RootView,
		ShowStats:       c.ShowStats,
		ShowByArtifact:  c.ShowByArtifact,
		ShowOrphaned:    c.ShowOrphaned,
		ShowByDate:      c.ShowByDate,
		ShowBySession:   c.ShowBySession,
		ShowByNamespace: c.ShowByNamespace,
		ShowRaw:         c.ShowRaw,
	}
}
