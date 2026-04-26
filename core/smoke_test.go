package core_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/testutil/driverfx"
	"github.com/rkurbatov/scrinium/internal/testutil/indexfx"
	"github.com/rkurbatov/scrinium/internal/testutil/storefx"
)

// TestSmoke_MillionSmallFiles is the M1 exit-criterion smoke:
// writing one million small files on a local SSD must run stably
// with O(1) memory (milestones, M1 exit clause).
//
// We exercise Put -> Walk-count -> Get-roundtrip on N unique
// payloads and assert two things:
//
//  1. Every artifact survives a round-trip: Walk sees N rows
//     and a sample subset reads back byte-identical content.
//
//  2. Heap usage stays bounded. Per Principle 7, "O(1) memory
//     under streaming" — Go-heap delta after a forced GC should
//     be a small constant, not proportional to N. The ceiling
//     below is conservative; if it ever fires, something started
//     accumulating per-artifact state in memory.
//
// This test must always run without -race: the path is single-
// threaded and the race detector adds ~10x overhead for nothing.
// The dedicated `make smoke` target enforces that; do not move
// this test into the generic test runner.
//
// Gated by testing.Short(). Override N via SCRINIUM_SMOKE_N for
// quicker triage runs.
func TestSmoke_MillionSmallFiles(t *testing.T) {
	if os.Getenv("SCRINIUM_SMOKE") != "1" {
		t.Skip("smoke: gated by SCRINIUM_SMOKE=1; run via `make smoke`")
	}

	const (
		// Default of 200k is enough to prove O(1) memory and
		// catch any per-artifact accumulation. The literal spec
		// target is 1M; that takes 12-15 minutes on Mac APFS and
		// is run explicitly via `make smoke N=1000000` before a
		// release, not on every smoke run.
		defaultN = 100_000
		// Each artifact: 32 bytes of unique content. Small enough
		// to keep on-disk total in the low-GB range, large enough
		// to exercise hashing + dedup paths.
		payloadSize = 32
		// Heap delta ceiling. With a disk-backed sqlite index
		// (modernc page cache ~8 MiB) and streaming Put, actual
		// delta on 1M is in the tens of MiB. 128 MiB gives healthy
		// headroom while still firing loudly on accidental O(N)
		// accumulation.
		heapDeltaCeiling = 128 << 20 // 128 MiB
		// Progress reports — emitted via emit() straight to stderr,
		// NOT via t.Logf. The testing package buffers per-test logs
		// until the test ends; gotestsum's "testname" format flushes
		// them in one batch at exit, which is useless for a multi-
		// minute run. Direct stderr writes show up live.
		reportEvery = 10_000
	)

	n := defaultN
	if v, ok := os.LookupEnv("SCRINIUM_SMOKE_N"); ok && v != "" {
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil && parsed > 0 {
			n = parsed
		}
	}
	emit("config: N=%d, payload=%dB, heap-ceiling=%s",
		n, payloadSize, humanBytes(heapDeltaCeiling))

	s, _ := newDiskStore(t)
	ctx := context.Background()

	// --- Baseline heap, before the loop ---
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	emit("baseline HeapAlloc: %s", humanBytes(int64(baseline.HeapAlloc)))

	// --- Put loop ---
	ids := make([]domain.ArtifactID, 0, 3) // first, mid, last for sample Get
	emit("Put: starting %d artifacts", n)
	startPut := time.Now()
	for i := 0; i < n; i++ {
		p := makePayload(i, payloadSize)
		id, err := s.Put(ctx,
			domain.Artifact{Payload: bytes.NewReader(p)},
			core.PutOptions{Namespace: "smoke"})
		if err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
		if i == 0 || i == n/2 || i == n-1 {
			ids = append(ids, id)
		}
		if (i+1)%reportEvery == 0 {
			reportProgress("Put", i+1, n, startPut)
		}
	}
	putElapsed := time.Since(startPut)
	emit("Put: done in %v (avg %.1f Put/s, %.1f us/Put)",
		putElapsed,
		float64(n)/putElapsed.Seconds(),
		float64(putElapsed.Microseconds())/float64(n))

	// --- Heap snapshot after the loop ---
	runtime.GC()
	var afterPut runtime.MemStats
	runtime.ReadMemStats(&afterPut)
	delta := int64(afterPut.HeapAlloc) - int64(baseline.HeapAlloc)
	emit("HeapAlloc after Put: %s (delta from baseline: %s)",
		humanBytes(int64(afterPut.HeapAlloc)), humanBytes(delta))
	if delta > heapDeltaCeiling {
		t.Errorf("heap delta %s exceeds ceiling %s — likely O(N) accumulation",
			humanBytes(delta), humanBytes(int64(heapDeltaCeiling)))
	}

	// --- Walk: every artifact visible through the index ---
	emit("Walk: counting manifests in 'smoke' namespace")
	startWalk := time.Now()
	var seen int
	if err := s.Walk(ctx, "smoke", func(_ domain.Manifest) error {
		seen++
		if seen%reportEvery == 0 {
			reportProgress("Walk", seen, n, startWalk)
		}
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	emit("Walk: %d manifests in %v (avg %.1f Walk/s)",
		seen, time.Since(startWalk),
		float64(seen)/time.Since(startWalk).Seconds())
	if seen != n {
		t.Errorf("Walk count: got %d, want %d", seen, n)
	}

	// --- Sample Get: round-trip the saved ids ---
	emit("Get: round-trip %d sampled artifacts", len(ids))
	for k, id := range ids {
		var sampleIdx int
		switch k {
		case 0:
			sampleIdx = 0
		case 1:
			sampleIdx = n / 2
		case 2:
			sampleIdx = n - 1
		}
		want := makePayload(sampleIdx, payloadSize)
		rh, err := s.Get(ctx, id, core.GetOptions{})
		if err != nil {
			t.Fatalf("Get sample #%d (id=%q): %v", sampleIdx, id, err)
		}
		got := readAllAndClose(t, rh)
		if !bytes.Equal(got, want) {
			t.Errorf("sample #%d: payload mismatch (got %d bytes, want %d)",
				sampleIdx, len(got), len(want))
		}
	}
	emit("Get: all %d samples matched", len(ids))

	emit("smoke OK: %d artifacts, total wall time %v", n, time.Since(startPut))
}

// emit writes one line straight to stderr, bypassing the testing
// package's per-test buffer. Using t.Logf in a long-running test
// means all output appears in one batch at the end — useless for
// live progress.
func emit(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "  "+format+"\n", args...)
}

// reportProgress emits one progress line. Same rationale as emit:
// direct stderr writes for live updates.
func reportProgress(op string, done, total int, start time.Time) {
	elapsed := time.Since(start)
	pct := float64(done) / float64(total) * 100
	rate := float64(done) / elapsed.Seconds()
	var eta time.Duration
	if rate > 0 {
		eta = time.Duration(float64(total-done)/rate) * time.Second
	}
	fmt.Fprintf(os.Stderr, "  %s: %d/%d (%.1f%%, %.0f/s, elapsed %v, ETA %v)\n",
		op, done, total, pct, rate,
		elapsed.Truncate(time.Second), eta.Truncate(time.Second))
}

// makePayload returns deterministic payloadSize-byte content: an
// 8-byte little-endian index followed by a recognisable filler.
// Each i yields a unique payload, so dedup never fires and the
// loop measures the worst case for index growth.
func makePayload(i int, size int) []byte {
	if size < 8 {
		panic("payload size must be >= 8")
	}
	p := make([]byte, size)
	binary.LittleEndian.PutUint64(p[:8], uint64(i))
	for j := 8; j < size; j++ {
		p[j] = byte(0xA0 | (j & 0x0F))
	}
	return p
}

func readAllAndClose(t *testing.T, rh core.ReadHandle) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rh); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if err := rh.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

// humanBytes formats a byte count as KB / MB / GB. Signed because
// heap deltas can be negative after a GC settles.
func humanBytes(n int64) string {
	abs := n
	sign := ""
	if abs < 0 {
		abs = -abs
		sign = "-"
	}
	const unit = 1024
	if abs < unit {
		return fmt.Sprintf("%s%d B", sign, abs)
	}
	div, exp := int64(unit), 0
	for x := abs / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%s%.1f %cB", sign,
		float64(abs)/float64(div), "KMGTPE"[exp])
}

// newDiskStore returns a Store whose index lives on disk, not in
// memory. Required for the million-files smoke: with :memory: the
// pure-Go modernc.org/sqlite holds every row in Go heap (~1+ GiB
// at 1M), which both blows our heap ceiling and risks OOM. A disk-
// backed index keeps page cache bounded (~8 MiB by default) and
// pushes data into the file, so HeapAlloc actually measures Put-
// side streaming behaviour.
func newDiskStore(t *testing.T) (core.Store, string) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	root := drv.Root()
	idx := indexfx.Disk(t, filepath.Join(t.TempDir(), "store.idx"))
	s := storefx.InitOn(t, drv, core.WithStoreIndex(idx))
	return s, root
}
