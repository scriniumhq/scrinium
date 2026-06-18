package store

import (
	"context"
	"log/slog"

	"scrinium.dev/domain"
	"scrinium.dev/internal/slogx"
)

// logging.go — the engine's logging foundation (ADR-60).
//
// The store writes diagnostics against *slog.Logger; the host picks the
// backend (text / JSON / zap / zerolog adapter) through its slog.Handler.
// There is no third-party logging dependency in this package's public API,
// no package-global logger, and no slog.Default() reach-through.
//
// Default is silence. A store built without WithLogger logs nowhere and
// pays nothing: the shared slogx.Discard wraps slog.DiscardHandler, whose
// Enabled always reports false. This package is the canonical instance of
// the system-wide model — the contract lives in docs/3 Reference/14 Logging.md,
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
		return slogx.Discard
	}
	return l.WithGroup("scrinium")
}

// logger returns the store's logger, never nil. Mirrors the nil-safety of
// publish: cheap and always callable.
func (c *core) logger() *slog.Logger {
	return slogx.OrDiscard(c.log)
}

// componentLogger derives a sublogger tagged with the component name
// (for example "store", "gc", "scrub"). Subcomponents and agents call
// this rather than re-deriving the group, so the "scrinium" group is
// applied exactly once (at construction) and the component attribute once
// here — a Handler formats a With attribute a single time.
func (c *core) componentLogger(component string) *slog.Logger {
	return c.logger().With(slog.String("component", component))
}

// optsLogger builds the lifecycle-phase logger for the package-level
// constructors (InitStore / OpenStore and their helpers), which run
// before a *store exists. It mirrors what buildStore will install on the
// store: resolveLogger applies the "scrinium" group, and the component
// tag marks the construction phase. Never nil — silent by default.
func optsLogger(o storeOptions, component string) *slog.Logger {
	return resolveLogger(o.logger).With(slog.String("component", component))
}

// --- redaction (ADR-60) ------------------------------------------------
//
// Secret-bearing values carry a LogValuer so that an accidental
// slog attribute can never spill key material, even if a future call site
// logs the whole value. The engine deliberately logs KeyID — an opaque
// identifier — and never the bytes behind it.

// redactedKey is the sentinel rendered in place of any secret byte
// material in a log record.
const redactedKey = "<redacted>"

// redactedSecret wraps any secret byte buffer (a DEK, a passphrase, a
// wrapped KEK) for logging. Its LogValue never exposes the bytes; it
// renders only the redaction sentinel. Use it when a secret would
// otherwise land in a slog attribute — the material stays out of the
// record while the attribute's presence remains visible.
//
// One type covers every secret: the buffers are byte-identical from the
// logger's point of view, so a per-secret type bought nothing but a name.
// The call site names the attribute (e.g. slog.Any("dek", redactedSecret(k)))
// where the role matters.
type redactedSecret []byte

// LogValue implements slog.LogValuer. The secret is never rendered.
func (redactedSecret) LogValue() slog.Value { return slog.StringValue(redactedKey) }

// keyIDAttr is the canonical, safe way to log a write/read key: by its
// opaque KeyID, never by its material. An empty KeyID (the default
// single-key resolver, or a non-crypto path) renders as "" rather than
// being omitted, so the absence is visible in the record.
func keyIDAttr(keyID string) slog.Attr {
	return slog.String("key_id", keyID)
}

// storeIDAttr is the conventional attribute identifying which Store a
// record came from. Safe: StoreID is a public UUID, not secret material.
func storeIDAttr(c *core) slog.Attr {
	return slog.String("store_id", c.storeID)
}

// manifestCryptoAttr records the crypto mode of an operation. Safe: the
// mode (Plain / Sealed / Paranoid) is not secret.
func manifestCryptoAttr(m domain.ManifestCrypto) slog.Attr {
	return slog.String("manifest_crypto", string(m))
}

// stateAttr records a Store state (e.g. Unlocked, Locked, Degraded).
// Safe: the state name carries no secret material.
func stateAttr(st domain.StoreState) slog.Attr {
	return slog.String("state", string(st))
}

// artifactIDAttr records an ArtifactID. Safe: it is a content-derived
// public identifier, not key material.
func artifactIDAttr(id domain.ArtifactID) slog.Attr {
	return slog.String("artifact_id", string(id))
}

// maintenanceModeAttr renders a MaintenanceMode by name (it is a uint8
// enum with no String method). Falls back to the numeric value for an
// unknown mode so a future addition still logs something meaningful.
func maintenanceModeAttr(m domain.MaintenanceMode) slog.Attr {
	switch m {
	case domain.MaintenanceModeNone:
		return slog.String("mode", "none")
	case domain.MaintenanceModeReadOnly:
		return slog.String("mode", "read_only")
	case domain.MaintenanceModeOffline:
		return slog.String("mode", "offline")
	default:
		return slog.Int("mode", int(m))
	}
}

// errAttr renders an error for a log record. Safe: engine errors are
// sentinel/format strings, never key material.
func errAttr(err error) slog.Attr {
	return slog.String("error", err.Error())
}

// traceErr logs an operation's error return at Debug and returns the
// error unchanged, so a call site reads `return s.traceErr(ctx, "Put", err, attrs...)`.
//
// This is the ADR-60 "Debug-on-error-return" pattern: the error is still
// returned to the caller (no swallowing), and the Debug record gives an
// operator a trace of WHICH boundary refused and WHY without the caller
// having to log it themselves. It is NOT log-and-return in the forbidden
// sense — that prohibition targets duplicate Warn/Error reporting; a
// Debug trace is silent in production (DiscardHandler / level>Debug) and
// exists purely for diagnostics. err must be non-nil.
func (c *core) traceErr(ctx context.Context, op string, err error, attrs ...slog.Attr) error {
	log := c.componentLogger("store")
	if log.Enabled(ctx, slog.LevelDebug) {
		rec := append([]slog.Attr{storeIDAttr(c), slog.String("op", op), errAttr(err)}, attrs...)
		log.LogAttrs(ctx, slog.LevelDebug, "operation failed", rec...)
	}
	return err
}
