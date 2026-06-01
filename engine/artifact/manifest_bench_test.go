package artifact_test

import (
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/testutil/manifestfx"
)

// Sinks defeat dead-code elimination so the benchmarked calls are not
// optimised away.
var (
	benchSinkBytes    []byte
	benchSinkManifest domain.Manifest
)

// BenchmarkManifestCodec tracks the CPU and allocation cost of the
// manifest codec — the hot path hit on every Put (encode) and every
// Get/Walk (decode), and the code the artifact-extraction refactor
// touched. It is intentionally stateless and deterministic: no disk, no
// growing index, no fsync. That makes B/op and allocs/op stable and the
// right signal to track with benchstat; ns/op is informative but
// machine- and load-dependent, so treat it as secondary.
//
// How to run and compare:
//
//	# record a run (10 iterations for benchstat's variance estimate)
//	go test -run=^$ -bench=^BenchmarkManifestCodec$ -benchmem -count=10 \
//	    ./engine/artifact/ | tee bench-new.txt
//	# diff against a saved baseline
//	benchstat bench-baseline.txt bench-new.txt
//
// The intended workflow: after the first run, paste the numbers into the
// BASELINE block below and commit them. On later changes, re-run and
// compare; a real regression shows as a consistent B/op or allocs/op
// shift (ns/op alone, drifting one way on one sub-bench and the other on
// another, is noise — see the smoke-test discussion).
//
// ---------------------------------------------------------------------
// BASELINE — machine-independent columns (B/op, allocs/op) are the
// signal to track; sec/op is comparable only on the same machine.
//
//	goos:    darwin
//	goarch:  arm64
//	pkg:     scrinium.dev/engine/artifact
//	cpu:     Apple M5
//	go:      1.26.2
//
//	BenchmarkManifestCodec/Encode-10    443.0n ± 5%    1.039Ki ± 0%     6.000 ± 0%
//	BenchmarkManifestCodec/Decode-10    2.006µ ± 2%    1.617Ki ± 0%    23.00 ± 0%
//
// Notes: Decode is ~4x Encode (reflection-based JSON Unmarshal, 23
// allocs/op) and is ~half the cost of one Walk step (Walk ~3.75us/
// manifest in the smoke). Encode is <0.3% of a Put (fsync dominates).
// The lever if Walk ever bottlenecks is ManifestEncodingBinary, not
// hand-tuning JSON. Watch Decode allocs/op for regressions — it is the
// number that feeds Walk throughput.
// ---------------------------------------------------------------------
func BenchmarkManifestCodec(b *testing.B) {
	m := manifestfx.Sample()

	// Pre-encode once for the Decode sub-benchmark's input.
	encoded, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		b.Fatalf("setup Encode: %v", err)
	}

	b.Run("Encode", func(b *testing.B) {
		b.ReportAllocs()
		var out []byte
		for i := 0; i < b.N; i++ {
			out, err = artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
			if err != nil {
				b.Fatal(err)
			}
		}
		benchSinkBytes = out
	})

	b.Run("Decode", func(b *testing.B) {
		b.ReportAllocs()
		var got domain.Manifest
		for i := 0; i < b.N; i++ {
			got, err = artifact.Decode(encoded)
			if err != nil {
				b.Fatal(err)
			}
		}
		benchSinkManifest = got
	})
}
