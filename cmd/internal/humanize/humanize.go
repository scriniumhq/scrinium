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

// BytesWithRaw renders a byte count as "<raw> (<human>)" — the exact
// integer followed by the humanised form in parentheses. Used in
// stats and metadata displays that want both the precise count and a
// readable size.
//
//	BytesWithRaw(1500) == "1500 (1.5 KiB)"
func BytesWithRaw(n int64) string {
	return fmt.Sprintf("%d (%s)", n, Bytes(n))
}

// BytesOrNA is BytesWithRaw, except a negative count (the sentinel a
// driver returns when capacity is unknown) renders as "n/a".
//
//	BytesOrNA(1500) == "1500 (1.5 KiB)"
//	BytesOrNA(-1)   == "n/a"
func BytesOrNA(n int64) string {
	if n < 0 {
		return "n/a"
	}
	return BytesWithRaw(n)
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
