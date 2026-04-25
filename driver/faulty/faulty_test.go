package faulty

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/driver/localfs"
)

func newWrapped(t *testing.T, opts ...Option) *Driver {
	t.Helper()
	inner, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatal(err)
	}
	return New(inner, opts...)
}

// TestPassthrough_NoFaults verifies the wrapper does not break the
// underlying driver in the absence of configured faults.
func TestPassthrough_NoFaults(t *testing.T) {
	d := newWrapped(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("data")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	r, err := d.Get(ctx, "f")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	r.Close()
	if d.CallCount(MethodPut) != 1 {
		t.Errorf("Put count = %d, want 1", d.CallCount(MethodPut))
	}
	if d.CallCount(MethodGet) != 1 {
		t.Errorf("Get count = %d, want 1", d.CallCount(MethodGet))
	}
}

// TestFailureRate_AlwaysFails verifies rate=1 deterministically
// fails. errors.Is(err, ErrInjected) is the documented contract.
func TestFailureRate_AlwaysFails(t *testing.T) {
	d := newWrapped(t,
		WithFailureRate(MethodPut, 1.0),
	)
	err := d.Put(context.Background(), "f", strings.NewReader("x"))
	if !errors.Is(err, ErrInjected) {
		t.Fatalf("expected ErrInjected, got %v", err)
	}
	// The call counter is still bumped on injected failure.
	if d.CallCount(MethodPut) != 1 {
		t.Errorf("counter not bumped on failure")
	}
}

// TestFailureRate_NeverFails verifies rate=0 never fails.
func TestFailureRate_NeverFails(t *testing.T) {
	d := newWrapped(t, WithFailureRate(MethodPut, 0))
	for i := 0; i < 50; i++ {
		if err := d.Put(context.Background(), "f", strings.NewReader("x")); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
}

// TestFailureRate_Deterministic verifies that the same seed
// produces the same fault sequence. This is what makes chaos tests
// reproducible.
func TestFailureRate_Deterministic(t *testing.T) {
	const N = 100
	const seed = 42
	pattern := func() []bool {
		d := newWrapped(t,
			WithSeed(seed),
			WithFailureRate(MethodGet, 0.5),
		)
		// Need a file to exist so non-faulty Gets don't return
		// ErrNotExist (which we'd confuse for an injected error).
		if err := d.Put(context.Background(), "f", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
		seq := make([]bool, N)
		for i := 0; i < N; i++ {
			_, err := d.Get(context.Background(), "f")
			seq[i] = errors.Is(err, ErrInjected)
		}
		return seq
	}
	a := pattern()
	b := pattern()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at i=%d: %v vs %v", i, a[i], b[i])
		}
	}
}

// TestFailureRate_Distribution checks that rate=0.5 produces
// roughly half failures over a large sample. We use a wide
// tolerance because we only want to catch gross misconfiguration,
// not statistical noise.
func TestFailureRate_Distribution(t *testing.T) {
	const N = 2000
	d := newWrapped(t,
		WithSeed(1),
		WithFailureRate(MethodPut, 0.5),
	)
	failed := 0
	for i := 0; i < N; i++ {
		if err := d.Put(context.Background(), "f", strings.NewReader("x")); errors.Is(err, ErrInjected) {
			failed++
		}
	}
	// With seed=1 and N=2000 the fraction is well within ±5%.
	min := int(0.45 * N)
	max := int(0.55 * N)
	if failed < min || failed > max {
		t.Errorf("expected ~%d–%d failures out of %d, got %d", min, max, N, failed)
	}
}

// TestPerMethodIsolation verifies that configuring one method does
// not leak into other methods.
func TestPerMethodIsolation(t *testing.T) {
	d := newWrapped(t,
		WithSeed(1),
		WithFailureRate(MethodGet, 1.0),
	)
	ctx := context.Background()
	// Put has no failure rate set.
	if err := d.Put(ctx, "f", strings.NewReader("x")); err != nil {
		t.Fatalf("Put unexpectedly failed: %v", err)
	}
	// Get has rate=1, must fail.
	if _, err := d.Get(ctx, "f"); !errors.Is(err, ErrInjected) {
		t.Fatalf("Get: expected ErrInjected, got %v", err)
	}
}

// TestLatency verifies that a configured latency is actually
// applied. We use a short latency so the test stays fast.
func TestLatency(t *testing.T) {
	const lat = 30 * time.Millisecond
	d := newWrapped(t, WithLatency(MethodPut, lat))
	start := time.Now()
	if err := d.Put(context.Background(), "f", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < lat {
		t.Errorf("Put returned in %v, expected at least %v", elapsed, lat)
	}
}

// TestLatency_RespectsContextCancellation: a cancelled context
// short-circuits the latency wait and returns ctx.Err.
func TestLatency_RespectsContextCancellation(t *testing.T) {
	d := newWrapped(t, WithLatency(MethodPut, time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := d.Put(ctx, "f", strings.NewReader("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("did not short-circuit on cancellation")
	}
}

// TestReset clears counters and re-seeds.
func TestReset(t *testing.T) {
	d := newWrapped(t, WithSeed(1))
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = d.Put(ctx, "f", strings.NewReader("x"))
	}
	if d.CallCount(MethodPut) != 5 {
		t.Errorf("count before reset: %d", d.CallCount(MethodPut))
	}
	d.Reset(2)
	if d.CallCount(MethodPut) != 0 {
		t.Errorf("count after reset: %d", d.CallCount(MethodPut))
	}
}

// TestCapabilitiesPassthrough ensures the wrapper does not lie
// about underlying capabilities. The chaos tool injects faults; it
// does not synthesise abilities the medium does not have.
func TestCapabilitiesPassthrough(t *testing.T) {
	d := newWrapped(t)
	caps := d.Capabilities()
	if caps == 0 {
		t.Error("expected at least the inner driver's capabilities")
	}
}
