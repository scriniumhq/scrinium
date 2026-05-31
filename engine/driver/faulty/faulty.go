package faulty

import (
	"context"
	"io"
	"math/rand"
	"sync"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

// Method names used by the configuration. They match the Driver
// interface methods one-to-one. Centralised here so tests can refer
// to them as constants and a typo does not silently disable a
// fault.
const (
	MethodPut                    = "Put"
	MethodGet                    = "Get"
	MethodReadAt                 = "ReadAt"
	MethodOpen                   = "Open"
	MethodRemove                 = "Remove"
	MethodRename                 = "Rename"
	MethodClone                  = "Clone"
	MethodStat                   = "Stat"
	MethodList                   = "List"
	MethodListObjectsWithModTime = "ListObjectsWithModTime"
	MethodCountObjects           = "CountObjects"
	MethodPruneEmptyDirs         = "PruneEmptyDirs"
	MethodMarkTombstone          = "MarkTombstone"
	MethodIsTombstone            = "IsTombstone"
	MethodTombstoneInfo          = "TombstoneInfo"
)

// The sentinel returned by injected faults (errs.ErrInjected) lives
// in the errs package; tests match it via errors.Is to distinguish
// injected failures from real backend errors.

// Driver wraps another driver.Driver and injects configurable
// faults. The zero value is unusable; construct via New.
type Driver struct {
	inner driver.Driver

	mu       sync.Mutex
	rng      *rand.Rand
	failures map[string]float64
	latency  map[string]time.Duration
	calls    map[string]int64
	failOn   map[string]int64
}

// Compile-time interface conformance check.
var _ driver.Driver = (*Driver)(nil)

// Option configures a faulty.Driver at construction time.
type Option func(*Driver)

// WithSeed makes the random fault stream deterministic. Without it
// the driver seeds from time.Now().UnixNano().
func WithSeed(seed int64) Option {
	return func(d *Driver) {
		d.rng = rand.New(rand.NewSource(seed))
	}
}

// WithFailureRate sets the probability that the named method
// returns errs.ErrInjected on each invocation. Rate must be in [0, 1];
// 0 disables, 1 always fails. Calling WithFailureRate again for
// the same method overwrites the previous value.
func WithFailureRate(method string, rate float64) Option {
	return func(d *Driver) {
		if rate < 0 {
			rate = 0
		}
		if rate > 1 {
			rate = 1
		}
		d.failures[method] = rate
	}
}

// WithLatency injects a blocking delay before the inner method
// is called. Useful for testing context-cancellation paths and
// exercising slow-medium code paths without a real network.
func WithLatency(method string, dur time.Duration) Option {
	return func(d *Driver) {
		d.latency[method] = dur
	}
}

// New wraps an inner driver. Pass options to configure faults.
func New(inner driver.Driver, opts ...Option) *Driver {
	d := &Driver{
		inner:    inner,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
		failures: make(map[string]float64),
		latency:  make(map[string]time.Duration),
		calls:    make(map[string]int64),
		failOn:   make(map[string]int64),
	}
	for _, fn := range opts {
		fn(d)
	}
	return d
}

// CallCount returns the number of times the named method has been
// invoked since construction (including failed calls). Used in
// tests to assert that the higher layer actually retried after a
// fault, fanned out to multiple targets, or short-circuited as
// expected.
func (d *Driver) CallCount(method string) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[method]
}

// Reset clears the call counters and re-seeds the random source.
// Per-method failure rates and latencies stay in place. Useful
// between test phases.
func (d *Driver) Reset(seed int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rng = rand.New(rand.NewSource(seed))
	for k := range d.calls {
		d.calls[k] = 0
	}
}

// gate is the central fault-injection point. It is invoked at the
// start of every wrapped method. When it returns a non-nil error,
// the wrapper returns immediately without calling the inner driver.
//
// gate also bumps the call counter and applies latency. Latency is
// applied even when no error is injected — it simulates the cost
// of the operation regardless of its outcome.
func (d *Driver) gate(ctx context.Context, method string) error {
	d.mu.Lock()
	d.calls[method]++
	trip := d.failOn[method] != 0 && d.calls[method] == d.failOn[method]
	rate := d.failures[method]
	lat := d.latency[method]
	var roll float64
	if rate > 0 {
		roll = d.rng.Float64()
	}
	d.mu.Unlock()
	if trip {
		return errs.ErrInjected
	}

	if lat > 0 {
		select {
		case <-time.After(lat):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if rate > 0 && roll < rate {
		return errs.ErrInjected
	}
	return nil
}

// --- Driver method wrappers ---

func (d *Driver) Put(ctx context.Context, path string, r io.Reader, opts ...driver.PutOption) error {
	if err := d.gate(ctx, MethodPut); err != nil {
		return err
	}
	return d.inner.Put(ctx, path, r, opts...)
}

func (d *Driver) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := d.gate(ctx, MethodGet); err != nil {
		return nil, err
	}
	return d.inner.Get(ctx, path)
}

func (d *Driver) ReadAt(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	if err := d.gate(ctx, MethodReadAt); err != nil {
		return nil, err
	}
	return d.inner.ReadAt(ctx, path, offset, size)
}

func (d *Driver) Open(ctx context.Context, uri string) (io.ReadCloser, error) {
	if err := d.gate(ctx, MethodOpen); err != nil {
		return nil, err
	}
	return d.inner.Open(ctx, uri)
}

func (d *Driver) Remove(ctx context.Context, path string) error {
	if err := d.gate(ctx, MethodRemove); err != nil {
		return err
	}
	return d.inner.Remove(ctx, path)
}

func (d *Driver) Rename(ctx context.Context, src, dst string) error {
	if err := d.gate(ctx, MethodRename); err != nil {
		return err
	}
	return d.inner.Rename(ctx, src, dst)
}

func (d *Driver) Clone(ctx context.Context, src, dst string) error {
	if err := d.gate(ctx, MethodClone); err != nil {
		return err
	}
	return d.inner.Clone(ctx, src, dst)
}

func (d *Driver) Stat(ctx context.Context, path string) (driver.FileInfo, error) {
	if err := d.gate(ctx, MethodStat); err != nil {
		return driver.FileInfo{}, err
	}
	return d.inner.Stat(ctx, path)
}

func (d *Driver) List(ctx context.Context, prefix string) ([]string, error) {
	if err := d.gate(ctx, MethodList); err != nil {
		return nil, err
	}
	return d.inner.List(ctx, prefix)
}

func (d *Driver) ListObjectsWithModTime(
	ctx context.Context,
	prefix string,
	since time.Time,
	cb func(driver.ObjectMeta) error,
) error {
	if err := d.gate(ctx, MethodListObjectsWithModTime); err != nil {
		return err
	}
	return d.inner.ListObjectsWithModTime(ctx, prefix, since, cb)
}

func (d *Driver) CountObjects(ctx context.Context, prefix string) (int64, error) {
	if err := d.gate(ctx, MethodCountObjects); err != nil {
		return 0, err
	}
	return d.inner.CountObjects(ctx, prefix)
}

func (d *Driver) PruneEmptyDirs(ctx context.Context, root string) error {
	if err := d.gate(ctx, MethodPruneEmptyDirs); err != nil {
		return err
	}
	return d.inner.PruneEmptyDirs(ctx, root)
}

func (d *Driver) Capabilities() driver.CapabilityMask {
	// Capabilities is static metadata, not an I/O operation. We
	// pass it through unchanged: a chaos test cannot lie about the
	// underlying medium's abilities.
	return d.inner.Capabilities()
}

func (d *Driver) MarkTombstone(ctx context.Context, path string) error {
	if err := d.gate(ctx, MethodMarkTombstone); err != nil {
		return err
	}
	return d.inner.MarkTombstone(ctx, path)
}

func (d *Driver) IsTombstone(ctx context.Context, path string) (bool, error) {
	if err := d.gate(ctx, MethodIsTombstone); err != nil {
		return false, err
	}
	return d.inner.IsTombstone(ctx, path)
}

func (d *Driver) TombstoneInfo(ctx context.Context, path string) (bool, time.Time, error) {
	if err := d.gate(ctx, MethodTombstoneInfo); err != nil {
		return false, time.Time{}, err
	}
	return d.inner.TombstoneInfo(ctx, path)
}

// WithFailOnCall makes the n-th invocation of method (1-based,
// counting from construction) return errs.ErrInjected exactly once.
// Deterministic and position-precise — the basis for torn-write sweeps.
// n <= 0 disables.
func WithFailOnCall(method string, n int64) Option {
	return func(d *Driver) {
		if n > 0 {
			d.failOn[method] = n
		}
	}
}

// SetFailOnCall installs the same deterministic trip at runtime, so a
// test can arm it AFTER bootstrap writes have run. Pairs with
// CallCount: arm at CallCount(m)+k to fail on the k-th write of the
// next operation.
func (d *Driver) SetFailOnCall(method string, n int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failOn[method] = n
}
