// Package artifactfx supplies test fixtures for the engine/artifact
// binary format: hash registries, DEKs, fake key providers, manifests
// carrying encryptable blocks, and on-disk encoded bytes.
//
// It layers on top of manifestfx (the dependency-free domain.Manifest
// builders) and engine/artifact (the format codec), mirroring the
// production layering store → artifact: tests build a manifest with
// manifestfx/artifactfx, encode it with artifactfx.Encoded, and feed the
// bytes to whatever is under test.
//
// Split of concerns:
//
//   - manifestfx: pure domain.Manifest values (no crypto, no codec).
//   - artifactfx: crypto + codec fixtures — Keys, DEK, Hashes, Encoded —
//     plus Manifest, which extends a manifestfx base with the ext/usr/
//     inline_blob blocks the encrypted-mode tests need to hide.
//
// artifactfx imports engine/artifact, so it must never be imported by
// engine/artifact production code — only by tests (engine/artifact's own
// external tests, store's tests, and any future format consumer).
package artifactfx
