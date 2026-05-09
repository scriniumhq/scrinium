// Package pathx contains tiny pure utilities for slash-separated
// paths. The path types here are logical: tree paths, projection
// paths, URL-segment paths — not OS filesystem paths (use
// path/filepath for those).
//
// Why not stdlib path? path.Base/path.Dir use unix-basename
// semantics: empty path becomes ".", "foo/" becomes "foo", "/"
// stays "/". Across this codebase logical paths use literal
// last-segment / first-segment semantics where empty/trailing-
// slash inputs produce empty output. Mixing the two would be a
// silent footgun. Callers that want unix-basename should reach
// for path.Base directly.
package pathx

import "strings"

// LastSegment returns everything after the last "/" in p, or p
// itself if p contains no slash. Empty input returns empty.
//
//	LastSegment("a/b/c")   == "c"
//	LastSegment("a")       == "a"
//	LastSegment("")        == ""
//	LastSegment("a/b/")    == ""
//	LastSegment("/")       == ""
func LastSegment(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Parent returns everything before the last "/" in p, or empty
// if p contains no slash.
//
//	Parent("a/b/c")  == "a/b"
//	Parent("a")      == ""
//	Parent("/a")     == ""
//	Parent("")       == ""
func Parent(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// SplitFirst returns the first segment and the remainder of p.
// For paths with no slash the entire input is the first segment
// and rest is "".
//
//	SplitFirst("a/b/c")  == ("a", "b/c")
//	SplitFirst("a")      == ("a", "")
//	SplitFirst("")       == ("", "")
//	SplitFirst("/a")     == ("", "a")
func SplitFirst(p string) (first, rest string) {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// Join glues parent and child with a single "/" between them.
// Empty parent returns child; empty child returns parent.
// Does not normalise existing slashes in either argument.
//
//	Join("a/b", "c")   == "a/b/c"
//	Join("", "c")      == "c"
//	Join("a", "")      == "a"
//	Join("", "")       == ""
func Join(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	return parent + "/" + child
}

// IsUnder reports whether p equals prefix or is a descendant of
// prefix (i.e. p == prefix || p starts with prefix+"/"). Empty
// prefix matches everything except empty p.
//
//	IsUnder("a/b/c", "a/b")  == true
//	IsUnder("a/b",   "a/b")  == true
//	IsUnder("a/bc",  "a/b")  == false
//	IsUnder("a",     "")     == true
//	IsUnder("",      "")     == false
func IsUnder(p, prefix string) bool {
	if prefix == "" {
		return p != ""
	}
	if p == prefix {
		return true
	}
	return strings.HasPrefix(p, prefix+"/")
}

// IsStrictUnder reports whether p is a strict descendant of
// prefix — equality is NOT a match. Use IsUnder to include
// equality.
//
//	IsStrictUnder("a/b/c", "a/b")  == true
//	IsStrictUnder("a/b",   "a/b")  == false
//	IsStrictUnder("a/bc",  "a/b")  == false
func IsStrictUnder(p, prefix string) bool {
	if prefix == "" {
		return p != ""
	}
	if len(p) <= len(prefix) {
		return false
	}
	if p[:len(prefix)] != prefix {
		return false
	}
	return p[len(prefix)] == '/'
}
