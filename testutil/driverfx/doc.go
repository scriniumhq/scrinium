// Package driverfx supplies driver fixtures for tests.
//
// LocalFS returns a localfs.Driver rooted at a fresh tempdir
// (cleaned up via t.Cleanup), saving every test from the
// boilerplate of MkdirTemp + RemoveAll + driver wiring.
//
// Other driver backends (s3, future faulty/lossy variants) will
// expose similar one-call constructors here as they land.
package driverfx
