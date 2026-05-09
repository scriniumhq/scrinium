// Package s3 will provide an S3-compatible Driver
// implementation. Status: NOT IMPLEMENTED.
//
// This package is reserved as the home of the future S3
// driver. When implementing:
//
//  1. Create localfs-equivalent Driver struct backed by an
//     S3 client (aws-sdk-go-v2 or minio-go).
//  2. Implement all 15 methods of driver.Driver against S3
//     primitives. Notable mappings:
//     - Put → PutObject; large bodies via UploadPartCopy.
//     - Get → GetObject (range support via Range header).
//     - Delete → DeleteObject.
//     - List → ListObjectsV2.
//     Latency, eventual consistency, and partial failures are
//     the operational concerns.
//  3. Add a register.go that calls
//     driver.RegisterDialer("s3", openS3URI).
//  4. URI form: s3://bucket/prefix?region=...&endpoint=...
//     Credentials should NOT travel in the URI; rely on the
//     standard SDK chain (env, profile, IAM role).
//
// Until the implementation lands, attempting to dial an
// s3:// URI returns "scheme s3 not registered" — the same
// uniform error as any other unregistered scheme.
//
// See driver/localfs for the reference Driver implementation.
package s3
