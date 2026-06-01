// Package stats composes and renders the Scrinium diagnostic stats
// report. It is a composition layer, not a primitive: it joins the
// projection's own counters (projection.ViewStats) with the store's
// physical capacity (domain.StorageInfo) and per-process runtime
// metadata, then renders the canonical text report served at
// _scrinium/stats.
//
// The report deliberately lives here rather than in projection: a
// projection owns its logical counters (ViewStats) but has no concept
// of physical capacity, registered index extensions, or daemon
// uptime. Those belong to the store and the assembly respectively, so
// the join is a composition concern — kept off the projection
// primitive's public surface.
package stats
