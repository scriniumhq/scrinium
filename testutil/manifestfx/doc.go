// Package manifestfx supplies builders for domain.Manifest and
// domain.PhysicalAddress values used in tests.
//
// Tests that need a "valid enough" manifest to feed into a
// projection, codec, or store call should use the builders here
// rather than hand-rolling Manifest literals — the latter break
// silently when the Manifest schema grows a new required field.
//
// The builders cover the common cases:
//
//   - Sample, Blob, BlobWithHash — increasingly explicit Manifest
//     constructors. Sample is the smallest "just give me a valid
//     manifest" call; Blob lets you pin id and blobRef; BlobWithHash
//     also pins the content hash and original size.
//   - SyntheticHash — fills a 32-byte ContentHash with a single
//     repeated byte, useful when only hash distinctness matters.
//   - PhysAddr, PackedAddr — PhysicalAddress builders for direct
//     and packed blob references.
package manifestfx
