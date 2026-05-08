package main

import (
	"strings"

	"github.com/rkurbatov/scrinium/internal/pathx"
)

// isOSJunk reports whether the last path segment of name matches
// a known noise pattern produced by macOS Finder, Windows
// Explorer, or other desktop OSes when they attach to a network
// share. The webdav daemon refuses Mkdir/Create/Rename targets
// with this name to prevent the store from accumulating
// hundreds of CAS blobs for trivially regenerated metadata.
//
// The check looks at the last segment only — `photos/.DS_Store`
// is junk; `photos/.DS_Store/sub` would be junk too because the
// junk part is its own segment and we'd be creating it. A user
// who legitimately wants to put a file *named* `.DS_Store` is
// out of luck — but in practice nobody does that.
//
// Patterns covered:
//
//	macOS Finder       .DS_Store, ._<anything> (AppleDouble),
//	                   .Spotlight-V100, .Trashes, .fseventsd,
//	                   .TemporaryItems, .localized,
//	                   .metadata_* (Spotlight family —
//	                   .metadata_never_index, ..._unless_rootfs,
//	                   ..._direct_scope_only, etc),
//	                   .apdisk, .AppleDouble, .AppleDB,
//	                   .AppleDesktop, .hidden,
//	                   Network Trash Folder, Temporary Items
//	Windows Explorer   Thumbs.db, desktop.ini, $RECYCLE.BIN,
//	                   System Volume Information
//	Microsoft Office   ~$<anything> (lock files for open docs)
func isOSJunk(name string) bool {
	seg := pathx.LastSegment(name)
	if seg == "" {
		return false
	}

	// AppleDouble: any name starting with "._".
	if strings.HasPrefix(seg, "._") {
		return true
	}
	// MS Office lock prefix.
	if strings.HasPrefix(seg, "~$") {
		return true
	}
	// Spotlight metadata family: .metadata_never_index,
	// .metadata_never_index_unless_rootfs,
	// .metadata_direct_scope_only, and any future variants
	// Apple decides to add. Match by prefix.
	if strings.HasPrefix(seg, ".metadata_") {
		return true
	}

	switch seg {
	case
		// macOS
		".DS_Store",
		".Spotlight-V100",
		".Trashes",
		".fseventsd",
		".TemporaryItems",
		".localized",
		".apdisk",
		".hidden",
		".AppleDouble",
		".AppleDB",
		".AppleDesktop",
		"Network Trash Folder",
		"Temporary Items",
		// Windows
		"Thumbs.db",
		"desktop.ini",
		"$RECYCLE.BIN",
		"System Volume Information":
		return true
	}
	return false
}
