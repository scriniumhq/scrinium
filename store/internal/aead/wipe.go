package aead

// Wipe overwrites b with zeros. Used for KEK, DEK, and passphrase
// buffers as soon as they are no longer needed. Defends against the
// trivial leakage path: a goroutine that holds a reference to a slice
// of memory after the engine is "done" with it.
//
// Note: Go's runtime can copy or relocate slice contents (escape
// analysis, GC compaction in future runtimes). Wipe is a best-effort
// hygiene measure, not a security guarantee. For threat models that
// require it (HSM-grade), use memguard-style locked memory in the
// host application.
//
// The wrapper around the clear() builtin is kept deliberately: the
// name documents intent at every call site (we are zeroing a secret,
// not initialising a slice).
//
// Lives in internal/aead — the shared low-level crypto home — so the
// store DEK/KEK lifecycle, the keyring, the key resolver, and the
// manifest codec all share one definition without depending on each
// other.
func Wipe(b []byte) {
	clear(b)
}
