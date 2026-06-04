// Package driver declares the Scrinium transport layer (L1):
// stateless adapters for accessing a Location of arbitrary nature
// (local filesystem, S3, network shares).
//
// A Driver translates the unified set of I/O operations into the
// concrete backend's API, guarantees atomicity of data commit
// (Rename for POSIX, CompleteMultipartUpload for S3) and reports
// its abilities through Capabilities.
//
// Concrete implementations (driver/localfs, driver/s3, driver/faulty)
// live in subpackages. This package contains only the interface
// contract and shared types.
//
// DAG: driver imports the standard library only. It does not import
// index, agent, or higher layers.
package driver
