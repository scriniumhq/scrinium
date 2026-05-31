// Package bundler implements the decorator that transparently packs
// small blobs into .pack volumes via a transit store.
//
// It intercepts blobs with size below DirectWriteThreshold, buffers
// them in a durable transit store, asynchronously assembles a
// .pack volume when a trigger fires (MaxBundleSize, MaxBlobCount,
// FlushInterval), and the MigrationAgent ships it to the destination
// store. Packing is transparent to the client: Put returns a regular
// ArtifactID, and Get knows how to range-read out of the pack.
//
// TODO(M3): blob bundling for many-small-files workloads.
package bundler

import (
	"context"
	"fmt"
	"time"

	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper/multistore"
	"scrinium.dev/errs"
)

// BundlerConfig holds the batch-sealing parameters. Triggers are
// disjunctive: the first satisfied condition starts Sealing.
type BundlerConfig struct {
	// TransitStore is the StoreID of the durable transit store that
	// accumulates small blobs before they are packed. When empty,
	// the wrapped Target plays the transit role (pack-in-place).
	TransitStore string

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

// New returns a WrapperFactory for registration as a Target decorator
// through WithStore/WithBackup.
//
// TODO(M3): bundling read path.
func New(cfg BundlerConfig) multistore.WrapperFactory {
	return &factory{cfg: cfg}
}

type factory struct {
	cfg BundlerConfig
}

func (f *factory) Wrap(store store.DataStore, deps multistore.WrapperDeps) (store.DataStore, error) {
	return nil, fmt.Errorf("%w: bundler.Wrap", errs.ErrNotImplemented)
}
