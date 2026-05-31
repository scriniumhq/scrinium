package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// RollbackSession is a group rollback of every artifact carrying the
// given SessionID:
//
//   - Sessions are correlation tags, not transactions. There is no
//     BeginSession / EndSession — clients pick a SessionID, attach
//     it to PutOptions, and later pass the same string here to
//     undo the batch.
//
//   - Atomic with respect to retention: if any artifact in the
//     session has an active RetentionUntil, nothing is deleted.
//
//   - Atomic with respect to DeletionPolicy: NoDelete refuses the
//     whole call.
//
//   - Idempotent: a re-issued call after an interrupted rollback
//     resumes from where the previous one stopped — the surviving
//     artifacts still match GetBySession; the deleted ones do not.
//
// Atomicity caveat (concurrent Put). The retention pre-check
// inspects the snapshot of the session as observed at call time.
// A concurrent Put that adds a new retention-bound artifact
// between the pre-check and the deletion loop will surface mid-
// loop as ErrRetentionNotExpired — by then a prefix of the
// session has already been deleted. This is structural for a
// system without session-level locks (sessions are correlation
// tags, not transactions). The next RollbackSession call observes
// only the surviving artifacts and proceeds normally.
func (d dataFacet) RollbackSession(ctx context.Context, sessionID domain.SessionID) error {
	if err := d.enterWrite(ctx); err != nil {
		return err
	}
	if sessionID == "" {
		// Defends against a mass-deletion mistake: the empty
		// string must NOT match every artifact "with no session"
		// and silently wipe the Store.
		return errs.ErrEmptySessionID
	}

	// 1. Resolve the artifact set through the index.
	ids, err := d.index.GetBySession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("store.RollbackSession: index lookup: %w", err)
	}
	if len(ids) == 0 {
		// No-op: session does not exist, or was already rolled
		// back. The "second call after success" idempotency
		// branch lands here. Nothing was rolled back, so no
		// EventRollbackCompleted is emitted.
		return nil
	}

	// 2. Atomic retention pre-check. Either every artifact is
	//    free of active retention, or the whole call refuses.
	now := time.Now()
	for _, id := range ids {
		m, err := d.loadManifest(ctx, id)
		if err != nil {
			// Index row exists but the manifest cannot be loaded —
			// inconsistent state; an index rebuild is the recovery path.
			return fmt.Errorf("store.RollbackSession: load %q: %w", id, err)
		}
		if !m.RetentionUntil.IsZero() && m.RetentionUntil.After(now) {
			return errs.ErrRetentionNotExpired
		}
	}

	// 3. Atomic DeletionPolicy pre-check. Mirrors Delete:
	//    DeletionPolicyNoDelete refuses regardless of retention.
	cfg := d.snapshotConfig()
	if cfg.DeletionPolicy == domain.DeletionPolicyNoDelete {
		return errs.ErrDeletionForbidden
	}

	// 4. Sequential Delete. Each call is its own atomic unit
	//    (manifest row + ref_count + file remove). A crash mid-
	//    loop leaves a partial rollback; the next call resumes.
	//
	//    A concurrent Delete between the pre-check and our
	//    Delete is the same shape as "already rolled back" —
	//    skip and continue.
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.Delete(ctx, id); err != nil {
			if errors.Is(err, errs.ErrArtifactNotFound) {
				continue
			}
			return fmt.Errorf("store.RollbackSession: delete %q: %w", id, err)
		}
	}

	// Lock-free summary: each Delete above already emitted its own
	// EventArtifactDeleted. Debug — the rolled-back batch size, for
	// diagnostics. SessionID is a client-chosen correlation tag, not
	// secret.
	d.componentLogger("store").LogAttrs(ctx, slog.LevelDebug, "session rolled back",
		storeIDAttr(d.core), slog.String("session_id", string(sessionID)),
		slog.Int("artifacts", len(ids)))

	// Group-level outcome event: one EventRollbackCompleted per
	// successful rollback, distinct from the per-artifact
	// EventArtifactDeleted stream. Emitted outside any lock.
	d.publish(event.EventRollbackCompleted, event.RollbackCompletedPayload{
		SessionID:  string(sessionID),
		RolledBack: len(ids),
	})
	return nil
}
