package core_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/internal/testutil/driverfx"
	"github.com/rkurbatov/scrinium/engine/internal/testutil/indexfx"
	"github.com/rkurbatov/scrinium/engine/internal/testutil/storefx"
)

// BenchmarkPut measures the Put pipeline's per-call cost across
// payload sizes that bracket the project's expected workloads:
//
//   - 64 B        — small metadata blob; pipeline overhead
//     dominates over hashing.
//   - 4 KiB       — typical filesystem page; the smallest
//     "real" content size.
//   - 64 KiB      — small file (config, image thumbnail).
//   - 1 MiB       — typical document.
//   - 16 MiB      — a large blob; pipeline behaviour at
//     sustained throughput.
//
// Each iteration writes a unique payload (b.N is mixed into the
// content) so the StoreIndex deduplication path is never hit —
// otherwise the second-and-onward iterations would short-circuit
// out of the actual write work.
//
// Goal: surface allocs/op and bytes/op trends. If allocs/op stays
// roughly constant as size grows, the pipeline is healthy. If it
// grows with size, there is a hot-path allocation worth a sync.Pool.
//
// Run:
//
//	go test ./core/ -bench=BenchmarkPut -benchmem -run=^$ -benchtime=2s
//
// For deeper analysis:
//
//	go test ./core/ -bench=BenchmarkPut_1MiB -benchmem -run=^$ \
//	    -cpuprofile=cpu.out -memprofile=mem.out
//	go tool pprof -http=: cpu.out
//	go tool pprof -http=: mem.out
//
// Baseline results (Apple M5, darwin/arm64, disk-backed sqlite,
// localfs driver with default fsync, 2026-05):
//
//	size       ns/op       MB/s     B/op      allocs/op
//	64 B       481K        0.13     48133     222
//	4 KiB      494K        8.30     48251     224
//	64 KiB     553K        118.5    48300     224
//	1 MiB      1.2M        855.8    48330     225
//	16 MiB     12.8M       1314.0   48813     229
//
// Read of these numbers:
//
//   - allocs/op is constant (222 → 229) across a 250000x range
//     in payload size. The pipeline does not allocate per-byte —
//     it streams. A sync.Pool for buffers would not help: there
//     are no per-payload buffers to pool.
//
//   - B/op is also constant (~48 KiB). The fixed cost is per-Put
//     overhead: SQLite statement objects, the manifest JSON
//     buffer, internal structs. Independent of payload size.
//
//   - Throughput at 1 MiB+ exceeds raw SSD write speed because
//     fsync is amortised by the filesystem and the OS page
//     cache absorbs writes during the bench window. Sustained
//     real-world throughput will be lower; this is a pipeline-
//     overhead measurement, not a disk benchmark.
//
//   - The 64 B / 4 KiB rows take essentially the same time
//     (~480 µs). For tiny blobs the pipeline is dominated by
//     the SQLite transaction (BEGIN + 2-3 INSERTs + COMMIT) and
//     localfs metadata operations (O_CREATE | O_EXCL + rename),
//     not by anything the Put pipeline itself does in CPU.
//
// Conclusion: do NOT add sync.Pool for hashers or buffers based
// on these numbers. The hypothesis that the pipeline is GC-
// pressured was not borne out. Re-measure if:
//
//   - the pipeline grows new transformations (compression,
//     encryption — M2);
//   - someone reports a real workload bottleneck pinned to
//     allocator pressure;
//   - migration to a different SQLite driver (modernc → cgo
//     mattn) materially changes the per-Put baseline.
//
// Until then, 222 allocs/op for a one-shot durable write through
// hash + JSON-encode + SQL + localfs is a reasonable cost.
func BenchmarkPut_64B(b *testing.B)   { benchmarkPut(b, 64) }
func BenchmarkPut_4KiB(b *testing.B)  { benchmarkPut(b, 4*1024) }
func BenchmarkPut_64KiB(b *testing.B) { benchmarkPut(b, 64*1024) }
func BenchmarkPut_1MiB(b *testing.B)  { benchmarkPut(b, 1*1024*1024) }
func BenchmarkPut_16MiB(b *testing.B) { benchmarkPut(b, 16*1024*1024) }

func benchmarkPut(b *testing.B, payloadSize int) {
	s := newDiskStoreForBench(b)
	ctx := context.Background()

	// Pre-allocate the payload buffer once. The first 8 bytes
	// are mutated per iteration to keep content unique without
	// re-allocating: bytes.NewReader wraps the slice without
	// copying, so a single buffer suffices across iterations.
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i)
	}

	b.SetBytes(int64(payloadSize))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Mix the iteration counter into the payload so each
		// Put writes unique content (avoiding dedup).
		binary.LittleEndian.PutUint64(payload[:8], uint64(i))
		_, err := s.Put(ctx,
			domain.Artifact{Payload: bytes.NewReader(payload)},
			domain.PutOptions{Namespace: "bench"})
		if err != nil {
			b.Fatalf("Put #%d: %v", i, err)
		}
	}
}

// newDiskStoreForBench mirrors smoke_test.go's newDiskStore but
// for benchmarks. The fixtures accept testing.TB, so *testing.B
// works directly. Disk-backed index keeps page cache bounded;
// in-memory index would inflate HeapAlloc over many iterations.
func newDiskStoreForBench(b *testing.B) core.Store {
	b.Helper()
	drv := driverfx.LocalFS(b)
	idx := indexfx.Disk(b, filepath.Join(b.TempDir(), "store.idx"))
	return storefx.InitOn(b, drv, core.WithStoreIndex(idx))
}
