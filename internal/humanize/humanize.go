// Package humanize formats numeric values for display.
//
// Currently scoped to byte counts. Other unit families (durations,
// counts) might join later if patterns repeat — for now the package
// stays small and focused on resolving the prior fragmentation
// where projection/stats, web/render and core/smoke_test each
// rendered byte counts with different precision and unit
// conventions, leading to user-visible inconsistencies (a stat
// shown as "1.42 MiB" in the terminal would appear as "1.4 MiB"
// in the web UI for the same store).
package humanize

import "fmt"

// Bytes renders a byte count in the largest IEC unit that keeps
// the value under 1024. Format is "<value> <unit>" with one
// decimal place for KiB and above, no decimal for raw B.
//
//	Bytes(0)        == "0 B"
//	Bytes(500)      == "500 B"
//	Bytes(1500)     == "1.5 KiB"
//	Bytes(1<<20)    == "1.0 MiB"
//
// Negative input is rendered with its absolute value (no sign).
// Use BytesSigned for diff/delta contexts where the sign matters.
func Bytes(n int64) string {
	if n < 0 {
		n = -n
	}
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
		TiB = 1024 * GiB
	)
	switch {
	case n >= TiB:
		return fmt.Sprintf("%.1f TiB", float64(n)/float64(TiB))
	case n >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(GiB))
	case n >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(MiB))
	case n >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(KiB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// BytesSigned is Bytes with an explicit sign for negative input.
// Used by diff/delta contexts (heap snapshot deltas after GC,
// before/after measurements) where direction is meaningful.
//
//	BytesSigned(1500)   == "1.5 KiB"
//	BytesSigned(-1500)  == "-1.5 KiB"
//	BytesSigned(0)      == "0 B"
func BytesSigned(n int64) string {
	if n < 0 {
		return "-" + Bytes(n)
	}
	return Bytes(n)
}
