// Package eventfx supplies a Recorder — a store.Publisher that
// captures every published event in memory — for tests that
// assert event behaviour.
//
// Typical pattern:
//
//	rec := eventfx.New()
//	store, _ := core.OpenStore(ctx, drv, core.WithPublisher(rec), ...)
//	// ... exercise the store ...
//	if got := rec.Count("manifest.put"); got != 1 {
//	    t.Errorf("expected 1 manifest.put event, got %d", got)
//	}
//
// Recorder is safe for concurrent Publish calls. ByType/All/Count
// return snapshots — callers are free to iterate while more
// events arrive.
package eventfx
