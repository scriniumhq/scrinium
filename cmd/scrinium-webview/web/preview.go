package web

import (
	"mime"
	"path"
	"strings"
)

// inlineableMIMEs is the conservative whitelist of MIME types
// the listing offers a [view] button for. Membership means
// "every modern desktop browser is known to render this inline
// without prompting the user". Anything not on the list is
// assumed to trigger a save-as dialog or play through a
// dedicated app — both unwanted on the diagnostic surface.
//
// Not on the list, even though common:
//   - image/avif: Safari support is recent (iOS 16+), not
//     "guaranteed" across browsers we encounter.
//   - application/json, application/xml: Chrome/Firefox render
//     them, Safari downloads them.
//   - video/*, audio/*: invokes a player, not a viewer. Users
//     wanting to play media should download and open in their
//     preferred app.
//
// Use isInlineable to test against this set; it folds the
// "text/* always wins" rule on top.
var inlineableMIMEs = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"image/png":       true,
	"image/gif":       true,
	"image/webp":      true,
	"image/svg+xml":   true,
	"text/plain":      true,
	"text/html":       true,
	"text/css":        true,
	"text/csv":        true,
	"text/markdown":   true,
}

// isInlineable reports whether the given MIME type is safe to
// link as a "view" target — i.e., the browser is expected to
// render it inline. text/* is always inlineable; everything
// else is checked against the conservative whitelist.
//
// Empty MIME returns false: when we don't know what the file
// is, we don't promise the browser will display it.
func isInlineable(mimeType string) bool {
	if mimeType == "" {
		return false
	}
	// text/plain;charset=utf-8 → "text/plain"
	base := mimeType
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	if strings.HasPrefix(base, "text/") {
		return true
	}
	return inlineableMIMEs[base]
}

// isImageInlineable is the image-only subset of isInlineable —
// matches just the image/* types from the whitelist. Used by
// the artifact page to decide whether to embed a <img> thumb.
// Reusing isInlineable plus a "image/" prefix check would also
// admit text/* and PDF, which we don't want as thumbnails.
func isImageInlineable(mimeType string) bool {
	if mimeType == "" {
		return false
	}
	base := mimeType
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	if !strings.HasPrefix(base, "image/") {
		return false
	}
	return inlineableMIMEs[base]
}

// inferMIME picks the best available MIME for a file. Priority:
//
//  1. The fsmeta-encoded MIME, if non-empty. Authoritative —
//     the producer set it explicitly.
//  2. The filename extension via mime.TypeByExtension. Fast,
//     no I/O, covers the common cases.
//  3. Empty string when nothing matches; isInlineable will
//     refuse to advertise [view] for these.
//
// We deliberately don't sniff magic bytes (http.DetectContentType
// on the first 512 B) here — that requires opening the blob,
// which the listing pipeline would do once per row. Sniffing is
// fine for the actual /_view/<id> endpoint, where we already
// have the bytes open.
func inferMIME(filename, fsmetaMIME string) string {
	if fsmetaMIME != "" {
		return fsmetaMIME
	}
	ext := path.Ext(filename)
	if ext == "" {
		return ""
	}
	return mime.TypeByExtension(ext)
}

// pathLastSegment returns everything after the last "/" in p,
// or p itself if there's no slash. Empty path returns "".
// Mirrors the projection helper of the same name; duplicated
// here to keep web a self-contained library that doesn't pull
// projection just for a one-line utility.
func pathLastSegment(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
