// Package projectionfx supplies a FakeSource — an in-memory
// implementation of store.Source — for unit tests that exercise
// the projection layer (View, FSOps) without standing up a real
// store.
//
// FakeSource lets tests:
//
//   - Add manifests and payloads programmatically.
//   - Inject errors on Walk/Get/Put/Delete (SetWalkErr etc.) to
//     verify projection's error-handling paths.
//   - Replay arbitrary manifest histories without driver/index
//     setup.
//
// Useful for testing host code that consumes store.Source — the
// FakeSource satisfies the same interface a real Store does on
// the projection-facing side.
//
// In-package tests of the projection package itself use this
// fixture too. External consumers (custom surfaces, integration
// tests, etc.) are welcome to use it for the same purpose.
package projectionfx
