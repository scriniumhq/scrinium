// Package bundler implements the decorator that transparently packs
// small blobs into .pack volumes through HostStorage.
//
// It intercepts blobs with size below DirectWriteThreshold, buffers
// them in HostStorage.system.transit, asynchronously assembles a
// .pack volume when a trigger fires (MaxBundleSize, MaxBlobCount,
// FlushInterval), and ships it to the Target as a single stream.
// Packing is transparent to the client: Put returns a regular
// ArtifactID, and Get knows how to range-read out of the pack.
//
// Moved from engine/curator/bundler per ADR-53: wrappers are a
// peer concept to Curator (a multi-store orchestrator) and live
// under engine/wrapper/.
//
// TODO(M4.4): blob bundling for many-small-files workloads.
package bundler

import (
	"context"
	"fmt"
	"time"

	"scrinium.dev/errs"
	"scrinium.dev/store"
	"scrinium.dev/store/wrapper/multistore"
)

// BundlerConfig holds the batch-sealing parameters. Triggers are
// disjunctive: the first satisfied condition starts Sealing.
type BundlerConfig struct {
	// MaxBundleSize is the maximum cumulative size of blobs in a
	// single .pack volume in bytes. The default is implementation
	// defined.
	MaxBundleSize int64

	// MaxBlobCount is the maximum number of blobs in a single .pack
	// volume.
	MaxBlobCount int

	// FlushInterval is the maximum age of an open batch. It guards
	// against perpetually open small batches under low load.
	FlushInterval time.Duration

	// DirectWriteThreshold sets the lower bound at which a blob is
	// written directly to the Target, bypassing the bundler. For
	// large blobs the packing overhead does not pay off.
	DirectWriteThreshold int64
}

// Wrapper is store.DataStore extended with an explicit Flush method
// for sealing the current batch on demand.
type Wrapper interface {
	store.DataStore

	// Flush seals the current batch immediately, regardless of
	// configuration triggers. Used before a graceful shutdown and
	// in tests.
	Flush(ctx context.Context) error
}

// New returns a WrapperFactory for registration in Curator through
// WithStore/WithBackup. It requires HostStorage in Curator: the
// factory's Wrap returns an error if deps.HostStorage == nil.
//
// TODO(M4.4): bundling read path.
func New(cfg BundlerConfig) multistore.WrapperFactory {
	return &factory{cfg: cfg}
}

type factory struct {
	cfg BundlerConfig
}

func (f *factory) Wrap(store store.DataStore, deps multistore.WrapperDeps) (store.DataStore, error) {
	return nil, fmt.Errorf("%w: bundler.Wrap", errs.ErrNotImplemented)
}
