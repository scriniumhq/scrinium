package errs

// sentinelError is the implementation behind every named scrinium
// sentinel. Beyond carrying its own Error() string, it can declare
// a list of bridge errors — typically io/fs sentinels — that
// errors.Is should match against.
//
// This lets host code write things like
//
//	if errors.Is(err, fs.ErrNotExist) { ... }
//
// without having to know which specific scrinium sentinel was
// returned. Path-not-found at the projection layer, artifact-not-
// found at the store layer, and store-not-found at the open layer
// all bridge to the same fs.ErrNotExist.
//
// Identity-based matching against the sentinel itself
// (errors.Is(err, errs.ErrPathNotFound)) keeps working: errors.Is
// walks the chain and compares the unwrapped value to the target
// pointer, which still hits this *sentinelError instance first.
type sentinelError struct {
	msg     string
	bridges []error
}

// Error returns the sentinel's stable message. Callers should not
// rely on the exact text; use errors.Is for matching.
func (e *sentinelError) Error() string {
	return e.msg
}

// Is reports whether target is one of this sentinel's declared
// bridges. The errors.Is mechanism handles wrapping and pointer
// identity itself; this only adds the bridge edges.
func (e *sentinelError) Is(target error) bool {
	for _, b := range e.bridges {
		if target == b {
			return true
		}
	}
	return false
}

// newSentinel constructs a sentinel with no bridge edges — a plain
// scrinium-only error with a stable identity.
func newSentinel(msg string) *sentinelError {
	return &sentinelError{msg: msg}
}

// newBridgedSentinel constructs a sentinel that errors.Is reports
// as matching one or more bridge targets in addition to itself.
//
// Typical use: bridging to io/fs sentinels so host code that knows
// only the standard library still gets correct semantics.
func newBridgedSentinel(msg string, bridges ...error) *sentinelError {
	return &sentinelError{msg: msg, bridges: bridges}
}
