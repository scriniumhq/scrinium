//go:build linux || darwin

package main

import (
	"hash/fnv"
	"strings"
)

// inodeFor maps a (tree, subPath) pair to a deterministic 64-bit inode
// number. fnv-64 suffices for a single mount session; go-fuse dedupes
// on (parent, name) at lookup, tolerating the rare collision. inode 1
// is the mount root (FUSE convention); the reserved low range (1..15)
// is avoided.
func inodeFor(tree string, subPath string) uint64 {
	if tree == "" && subPath == "" {
		return 1
	}
	h := fnv.New64a()
	h.Write([]byte(tree))
	h.Write([]byte{0})
	h.Write([]byte(subPath))
	v := h.Sum64()
	if v < 16 {
		v += 16
	}
	return v
}

// cleanName strips surrounding slashes (defensive; go-fuse passes bare
// names).
func cleanName(s string) string {
	return strings.Trim(s, "/")
}
