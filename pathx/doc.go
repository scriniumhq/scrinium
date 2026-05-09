// Package pathx contains tiny pure utilities for slash-separated
// logical paths — tree paths, projection paths, URL segments. It
// is NOT for OS filesystem paths; for those, use path/filepath
// from the standard library.
//
// # Why not stdlib path
//
// path.Base / path.Dir use unix-basename semantics: empty path
// becomes ".", "foo/" becomes "foo", "/" stays "/". pathx uses
// literal last-segment / first-segment semantics where empty or
// trailing-slash inputs produce empty output, matching how
// Scrinium represents tree paths. Mixing the two would be a
// silent footgun. Callers that want unix-basename should reach
// for path.Base directly.
//
// All functions are pure: no I/O, no allocations beyond the
// returned slice / string. Safe for concurrent use.
package pathx
