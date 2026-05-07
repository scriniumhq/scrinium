package web

import "fmt"

// HumanSize renders a byte count in the largest unit that keeps
// the value under 1024. Public so callers building richer pages
// (artifact details, stats) can reuse it.
//
// Mirrors projection.RenderStats's helper but kept here to keep
// web a self-contained library — no need to drag projection in
// only for a one-line utility.
func HumanSize(n int64) string {
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
