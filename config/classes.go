package config

import (
	"fmt"
	"strings"

	"scrinium.dev/config/internal/fieldkit"
	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// Parameter classes (ADR-110). Every StoreConfig field belongs to
// exactly one class; the classification is engine knowledge, not a
// config axis.
//
//   - Class I — immutable: fixed at InitStore, changed only by
//     rebuilding the store. Compared by ValidateAgainstActive
//     (model.go); divergence → ErrConfigMismatch.
//   - Class II — admin-mutable governance defaults: DeletionPolicy,
//     RetentionPeriod, TombstoneGracePeriod, GCLeasePolicy,
//     SessionOverrides. Changed only by an explicit admin act
//     (UpdateConfig); a CONNECTING client's diverging value is refused
//     with ErrGovernanceMismatch — retention cannot be escaped by
//     connecting with a softer config.
//   - Class III — user-mutable session preferences, self-describing
//     per artifact (LayoutHeader/stages frozen in the manifest):
//     BlobStorage, VerifyOnRead, InlineBlobLimit, PackAlignment,
//     EagerFetchLimit, Pipeline (non-crypto prefix). Target semantics:
//     a session overlay — the connection lives by its own values.
//
// The same non-zero convention as ValidateAgainstActive applies
// everywhere: only fields the caller actually populated are compared;
// a zero value means "not asked" and passes. There is no silent-ignore
// outcome anywhere (INV-110-5): every populated field is either
// applied, matched, or refused.

// PlanConnection is the OpenStore-side check of a client config
// against the store's active one, per class:
//
//	class I  diverges → ErrConfigMismatch     (via ValidateAgainstActive)
//	class II diverges → ErrGovernanceMismatch (admin path named in the error)
//	class III populated → returned as the session overlay — the
//	    connection lives by its own values (in memory, never
//	    persisted). Under SessionOverrides=Deny a DIVERGING class-III
//	    field is refused like class II. A client Pipeline must carry
//	    the store's crypto tail unchanged (first Rules-Engine rule,
//	    ADR-110): the security posture is not overridable by
//	    connecting.
//
// UpdateConfig deliberately does NOT use this: changing class II is
// its very purpose; it keeps calling ValidateAgainstActive (class I
// only).
func PlanConnection(req, active domain.StoreConfig) (domain.StoreConfig, error) {
	if err := ValidateAgainstActive(req, active); err != nil {
		return domain.StoreConfig{}, err
	}

	if gov := divergentGovernance(req, active); len(gov) > 0 {
		return domain.StoreConfig{}, fmt.Errorf(
			"%w: %s — governance defaults (class II) change only by an explicit admin act (UpdateConfig), not by connecting",
			errs.ErrGovernanceMismatch, strings.Join(gov, "; "))
	}

	ses := divergentSession(req, active)
	if len(ses) > 0 && active.SessionOverrides == domain.SessionOverridesDeny {
		return domain.StoreConfig{}, fmt.Errorf(
			"%w: SessionOverrides=Deny — session overrides (class III) are disabled on this store; diverging field(s): %s",
			errs.ErrGovernanceMismatch, strings.Join(ses, "; "))
	}

	if len(req.Pipeline) > 0 {
		if err := validateCryptoTail(req.Pipeline, active.Pipeline); err != nil {
			return domain.StoreConfig{}, err
		}
	}
	return sessionOverlay(req), nil
}

// sessionOverlay extracts the populated class-III fields of req — the
// per-connection overlay. Only class III ever lands here, so merging
// it can never touch identity, crypto form, or governance.
func sessionOverlay(req domain.StoreConfig) domain.StoreConfig {
	return domain.StoreConfig{
		BlobStorage:     req.BlobStorage,
		VerifyOnRead:    req.VerifyOnRead,
		InlineBlobLimit: req.InlineBlobLimit,
		PackAlignment:   req.PackAlignment,
		EagerFetchLimit: req.EagerFetchLimit,
		Pipeline:        req.Pipeline,
	}
}

// MergeSession lays a session overlay over the active defaults:
// populated overlay fields win, everything else — the store's
// defaults. Explicitly class III only.
func MergeSession(active, overlay domain.StoreConfig) domain.StoreConfig {
	eff := active
	if overlay.BlobStorage != "" {
		eff.BlobStorage = overlay.BlobStorage
	}
	if overlay.VerifyOnRead != "" {
		eff.VerifyOnRead = overlay.VerifyOnRead
	}
	if overlay.InlineBlobLimit != 0 {
		eff.InlineBlobLimit = overlay.InlineBlobLimit
	}
	if overlay.PackAlignment != 0 {
		eff.PackAlignment = overlay.PackAlignment
	}
	if overlay.EagerFetchLimit != 0 {
		eff.EagerFetchLimit = overlay.EagerFetchLimit
	}
	if len(overlay.Pipeline) > 0 {
		eff.Pipeline = overlay.Pipeline
	}
	return eff
}

// validateCryptoTail enforces the ADR-110 Pipeline rule: the store's
// crypto tail (the trailing crypto stages of the active pipeline) must
// appear, unchanged and in order, as the suffix of the client
// pipeline; and the client must not smuggle crypto stages of its own
// elsewhere. The non-crypto prefix (compression) is the session's
// freedom.
func validateCryptoTail(req, active []string) error {
	tail := cryptoTail(active)

	if len(tail) > 0 {
		if len(req) < len(tail) {
			return fmt.Errorf(
				"%w: client pipeline %v drops the store crypto tail %v (class I derivative, ADR-110)",
				errs.ErrConfigMismatch, req, tail)
		}
		for i := range tail {
			if req[len(req)-len(tail)+i] != tail[i] {
				return fmt.Errorf(
					"%w: client pipeline %v does not end with the store crypto tail %v (class I derivative, ADR-110)",
					errs.ErrConfigMismatch, req, tail)
			}
		}
	}
	// No crypto stages outside the mandated tail — neither dropping
	// nor smuggling changes the store's security posture.
	for _, algo := range req[:len(req)-len(tail)] {
		if domain.IsCryptoAlgorithm(algo) {
			return fmt.Errorf(
				"%w: client pipeline %v adds crypto stage %q outside the store crypto tail (ADR-110)",
				errs.ErrConfigMismatch, req, algo)
		}
	}
	return nil
}

// cryptoTail returns the maximal all-crypto suffix of a pipeline.
func cryptoTail(p []string) []string {
	i := len(p)
	for i > 0 && domain.IsCryptoAlgorithm(p[i-1]) {
		i--
	}
	return p[i:]
}

// divergentGovernance lists populated class-II fields of req that
// differ from active. Derived from the registry filtered by class —
// there is no separate hand-written field list here.
func divergentGovernance(req, active domain.StoreConfig) []string {
	return fieldkit.DivergentByClass(registry, ClassGovernance, req, active)
}

// divergentSession lists populated class-III fields of req that differ
// from active. Same registry, filtered to class III. PackAlignment's
// zero-vs-None ambiguity is preserved exactly as before: zero counts
// as "not asked" (the registry's diverges treats the Go zero as unset).
func divergentSession(req, active domain.StoreConfig) []string {
	return fieldkit.DivergentByClass(registry, ClassSession, req, active)
}

func equalPipelines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
