package store

import (
	"log/slog"

	"scrinium.dev/engine/domain"
)

// logging.go — the engine's logging foundation (ADR-60).
//
// The store writes diagnostics against *slog.Logger; the host picks the
// backend (text / JSON / zap / zerolog adapter) through its slog.Handler.
// There is no third-party logging dependency in this package's public API,
// no package-global logger, and no slog.Default() reach-through.
//
// Default is silence. A store built without WithLogger logs nowhere and
// pays nothing: discardLogger wraps slog.DiscardHandler, whose Enabled
// always reports false. This package is the canonical instance of the
// system-wide model — the contract lives in docs/3 Reference/14 Logging.md,
// the cross-layer invariants in docs/2 Internals/13 Concurrency Model.md.
//
// Three rules this package follows (and that sibling files must keep):
//
//   - logs explain, events notify, errors return. No log-and-return: an
//     error is either returned to the caller or (in a callerless
//     background path) logged, never both.
//   - logger calls happen OUTSIDE any held mutex — a synchronous Handler
//     under a lock is as dangerous as a slow event subscriber
//     (Concurrency Model invariant 7).
//   - key material never reaches a log. Secret-bearing values implement
//     slog.LogValuer and redact themselves; only KeyID (an opaque id) is
//     logged, never the DEK / passphrase / KEK.

// discardLogger is the shared no-op logger used whenever the host did not
// supply one. slog.DiscardHandler (Go 1.24+) reports Enabled == false, so
// every call short-circuits before any argument is formatted.
var discardLogger = slog.New(slog.DiscardHandler)

// WithLogger provides the *slog.Logger the Store and its components log
// against. Optional: without it the Store is silent (slog.DiscardHandler).
// A nil logger is treated as "silent" — WithLogger(nil) never panics and
// is equivalent to omitting the option.
//
// The Store namespaces the supplied logger once at construction
// (WithGroup("scrinium")) and derives per-component subloggers from it
// (componentLogger). Hosts therefore pass a plain root logger; the engine
// adds its own structure.
func WithLogger(l *slog.Logger) StoreOption {
	return func(o *storeOptions) { o.logger = l }
}

// resolveLogger turns the (possibly nil) injected logger into the
// store's namespaced logger. Called once, from buildStore. Never returns
// nil — callers can log unconditionally and rely on Enabled-gating for
// cost on hot paths.
func resolveLogger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return discardLogger
	}
	return l.WithGroup("scrinium")
}

// logger returns the store's logger, never nil. Mirrors the nil-safety of
// publish: cheap and always callable.
func (s *store) logger() *slog.Logger {
	if s.log == nil {
		return discardLogger
	}
	return s.log
}

// componentLogger derives a sublogger tagged with the component name
// (for example "store", "gc", "scrub"). Subcomponents and agents call
// this rather than re-deriving the group, so the "scrinium" group is
// applied exactly once (at construction) and the component attribute once
// here — a Handler formats a With attribute a single time.
func (s *store) componentLogger(component string) *slog.Logger {
	return s.logger().With(slog.String("component", component))
}

// --- redaction (ADR-60 §"Безопасность", Concurrency Model §3.4) ---------
//
// Secret-bearing values carry a LogValuer so that an accidental
// slog attribute can never spill key material, even if a future call site
// logs the whole value. The engine deliberately logs KeyID — an opaque
// identifier — and never the bytes behind it.

// redactedKey is the sentinel rendered in place of any secret byte
// material in a log record.
const redactedKey = "<redacted>"

// redactedDEK wraps a data-encryption key for logging. Its LogValue never
// exposes the bytes; it reports only that a key is present and its length
// class is hidden. Use this if a DEK ever needs to appear in a log
// attribute — the bytes themselves stay out of the record.
type redactedDEK []byte

// LogValue implements slog.LogValuer. The DEK is never rendered.
func (redactedDEK) LogValue() slog.Value { return slog.StringValue(redactedKey) }

// redactedPassphrase wraps a passphrase buffer for logging. Same
// discipline as redactedDEK: the buffer is never rendered.
type redactedPassphrase []byte

// LogValue implements slog.LogValuer. The passphrase is never rendered.
func (redactedPassphrase) LogValue() slog.Value { return slog.StringValue(redactedKey) }

// keyIDAttr is the canonical, safe way to log a write/read key: by its
// opaque KeyID, never by its material. An empty KeyID (the default
// single-key resolver, or a non-crypto path) renders as "" rather than
// being omitted, so the absence is visible in the record.
func keyIDAttr(keyID string) slog.Attr {
	return slog.String("key_id", keyID)
}

// storeIDAttr is the conventional attribute identifying which Store a
// record came from. Safe: StoreID is a public UUID, not secret material.
func storeIDAttr(s *store) slog.Attr {
	return slog.String("store_id", s.storeID)
}

// manifestCryptoAttr records the crypto mode of an operation. Safe: the
// mode (Plain / Sealed / Paranoid) is not secret.
func manifestCryptoAttr(m domain.ManifestCrypto) slog.Attr {
	return slog.String("manifest_crypto", string(m))
}
