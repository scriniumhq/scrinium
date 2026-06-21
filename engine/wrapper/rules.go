package wrapper

import "fmt"

// ValidateOptions carries the assembly-time facts the Rules Engine needs
// beyond the wrapper descriptors themselves.
type ValidateOptions struct {
	// OnBackup is true when the stack is applied to a Backup target. A
	// chunker is forbidden there: its TOC yields a different ArtifactID,
	// which breaks cross-store deduplication (ADR-75).
	OnBackup bool

	// Size-invariant inputs (bytes). When all three are non-zero the Rules
	// Engine enforces MaxChunkSize ≤ DirectWriteThreshold ≤ MaxBundleSize
	// (ADR-75). A zero means "not supplied" and skips the check; the
	// values are threaded once chunker/bundler config is wired (M4.5/M4.6).
	MaxChunkSize         int
	DirectWriteThreshold int
	MaxBundleSize        int
}

// structuralSet is the closed set of structural wrapper names (ADR-75).
var structuralSet = map[string]bool{"chunker": true, "bundler": true}

// Validate checks a wrapper stack at assembly time — not at runtime
// (ADR-75, "Composition validation"). It enforces:
//   - the structural set is closed to {chunker, bundler};
//   - at most one of each structural wrapper (no duplicate chunker/bundler);
//   - the forced structural order chunker → bundler → store;
//   - a chunker is not applied on a Backup target;
//   - the size invariant MaxChunkSize ≤ DirectWriteThreshold ≤ MaxBundleSize.
//
// Behavioral wrappers are order-free and unconstrained here. Per-wrapper
// config validation lives in each Factory's Wrap, not in the Rules Engine.
//
// stack is in apply order — innermost first (closest to the store). Data
// flows through the outermost wrapper first, so the forced
// chunker → bundler → store data flow means bundler is applied before
// chunker (chunker is the outermost decorator).
func Validate(stack []Descriptor, opts ValidateOptions) error {
	chunkerAt, bundlerAt := -1, -1
	for i, d := range stack {
		if d.Class != Structural {
			continue
		}
		if !structuralSet[d.Name] {
			return fmt.Errorf("wrapper %q is structural but not in the closed set {chunker, bundler}; a new structural wrapper requires a new ADR", d.Name)
		}
		switch d.Name {
		case "chunker":
			if chunkerAt >= 0 {
				return fmt.Errorf("more than one chunker in the stack")
			}
			chunkerAt = i
		case "bundler":
			if bundlerAt >= 0 {
				return fmt.Errorf("more than one bundler in the stack")
			}
			bundlerAt = i
		}
	}

	// Forced order chunker → bundler → store: chunker is the outermost
	// decorator, so in apply order (innermost first) it must come after
	// the bundler.
	if chunkerAt >= 0 && bundlerAt >= 0 && chunkerAt < bundlerAt {
		return fmt.Errorf("structural order violated: chunker must wrap bundler (chunker → bundler → store)")
	}

	if opts.OnBackup && chunkerAt >= 0 {
		return fmt.Errorf("chunker is not allowed on a Backup target: its TOC yields a different ArtifactID and breaks cross-store deduplication")
	}

	if opts.MaxChunkSize > 0 && opts.DirectWriteThreshold > 0 && opts.MaxBundleSize > 0 {
		if opts.MaxChunkSize > opts.DirectWriteThreshold || opts.DirectWriteThreshold > opts.MaxBundleSize {
			return fmt.Errorf("size invariant violated: require MaxChunkSize (%d) <= DirectWriteThreshold (%d) <= MaxBundleSize (%d)", opts.MaxChunkSize, opts.DirectWriteThreshold, opts.MaxBundleSize)
		}
	}

	return nil
}
