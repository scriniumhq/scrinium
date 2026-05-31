// Command scrinium-fuse mounts a Scrinium store as a POSIX-shaped
// filesystem via FUSE (Linux/macOS only; Windows users run
// scrinium-webdav).
//
// The store/projection is described by a Scrinium configuration document; the
// mount point and FUSE options are flags. The config says WHAT is
// stored and how it is projected; the daemon decides WHERE and HOW to
// mount it.
//
//	scrinium-fuse mount   --config store.yaml --mount-point /mnt/scrinium
//	scrinium-fuse unmount --mount-point /mnt/scrinium
//
// This file is a reference implementation: small and self-contained,
// meant to be copied and adapted. The reusable parts live in scrinium
// (assembly) and engine/projection; the FUSE node tree (node.go) and
// this glue are yours to own.
package main
