package rebuild

import (
	"time"
)

// RebuildStats are the final statistics of the operation and a
// progress snapshot.
type RebuildStats struct {
	// Source is the path actually taken (relevant for Auto).
	Source RebuildSource

	// CheckpointUsed is the checkpoint ID when Source != FullScan; an
	// empty string when starting from scratch.
	CheckpointUsed string

	// ManifestsScanned — total manifests read from the Location.
	ManifestsScanned int64

	// ManifestsIndexed — added to the StoreIndex.
	ManifestsIndexed int64

	// ManifestsSkipped — already in the checkpoint, not re-read.
	ManifestsSkipped int64

	// BlobsRegistered — rows in the blobs table (regular + chunks).
	BlobsRegistered int64

	// PacksIndexed — pack volume TOCs read and parsed.
	PacksIndexed int64

	// PointerRecovered — was system.config/current restored?
	PointerRecovered bool

	// DescriptorRewrote — was store.json rewritten from the
	// Recovery Kit?
	DescriptorRewrote bool

	// Duration is the operation's elapsed time.
	Duration time.Duration
}

// Stats returns a snapshot of progress (safe to call concurrently).
func (a *rebuildAgent) Stats() RebuildStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

func (a *rebuildAgent) setSource(s RebuildSource) {
	a.mu.Lock()
	a.stats.Source = s
	a.mu.Unlock()
}

func (a *rebuildAgent) setCheckpointUsed(name string) {
	a.mu.Lock()
	a.stats.CheckpointUsed = name
	a.mu.Unlock()
}

func (a *rebuildAgent) countScanned() {
	a.mu.Lock()
	a.stats.ManifestsScanned++
	a.mu.Unlock()
}

func (a *rebuildAgent) countIndexed(registeredBlob bool) {
	a.mu.Lock()
	a.stats.ManifestsIndexed++
	if registeredBlob {
		a.stats.BlobsRegistered++
	}
	a.mu.Unlock()
}
