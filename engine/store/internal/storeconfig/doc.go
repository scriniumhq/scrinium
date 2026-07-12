// Package storeconfig is the persistence of the store's configuration:
// versioned store.config.<seq> cells in the named space (Write, Read,
// History, ActiveSeq). It is engine plumbing — HOW the store keeps its
// config on disk. The configuration MODEL — classes, defaults,
// validation, connection planning — lives in the public top-level
// package config (scrinium.dev/config), the single entry point for
// every consumer of the StoreConfig axis.
package storeconfig
