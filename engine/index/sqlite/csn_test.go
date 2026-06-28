package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// TestCSN_BaselineCounter checks the v1 baseline carries the change-
// sequence state ADR-106 rides on: the manifests.csn column, the
// index_seq counter table seeded to a single (0, 0, 0) row, and the
// manifests_csn index.
func TestCSN_BaselineCounter(t *testing.T) {
	idx := newMemoryIndex(t)
	ctx := context.Background()

	// manifests.csn resolves — the column is present (a zero-row select
	// errors at prepare time when the column is missing). Close the Rows:
	// an unclosed result set keeps the connection checked out of the pool,
	// and a :memory: pool hands the next query a fresh, empty database.
	probeRows, err := idx.db.QueryContext(ctx, `SELECT csn FROM manifests LIMIT 0`)
	if err != nil {
		t.Fatalf("manifests.csn column missing: %v", err)
	}
	_ = probeRows.Close()
	if !objectExists(t, idx, "table", "index_seq") {
		t.Error("baseline missing table index_seq")
	}
	if !objectExists(t, idx, "index", "manifests_csn") {
		t.Error("baseline missing index manifests_csn")
	}

	// index_seq is a single pinned row, seeded to zero.
	var rows int
	if err := idx.db.QueryRowContext(ctx, `SELECT count(*) FROM index_seq`).Scan(&rows); err != nil {
		t.Fatalf("count index_seq: %v", err)
	}
	if rows != 1 {
		t.Fatalf("index_seq rows = %d, want 1", rows)
	}
	var csn, prune uint64
	if err := idx.db.QueryRowContext(ctx,
		`SELECT csn, prune_csn FROM index_seq WHERE id = 0`,
	).Scan(&csn, &prune); err != nil {
		t.Fatalf("read index_seq: %v", err)
	}
	if csn != 0 || prune != 0 {
		t.Errorf("seed index_seq = (csn=%d, prune=%d), want (0, 0)", csn, prune)
	}
}

// TestCSN_NextMonotonic checks nextCSN issues a strictly increasing value,
// both within one transaction and across transactions, and that readToken
// reflects the last issued value.
func TestCSN_NextMonotonic(t *testing.T) {
	idx := newMemoryIndex(t)
	ctx := context.Background()

	var got []uint64
	// Two issues inside one transaction.
	if err := idx.inTx(ctx, func(tx *sql.Tx) error {
		a, err := nextCSN(ctx, tx)
		if err != nil {
			return err
		}
		b, err := nextCSN(ctx, tx)
		if err != nil {
			return err
		}
		got = append(got, a, b)
		return nil
	}); err != nil {
		t.Fatalf("inTx #1: %v", err)
	}
	// A third issue in a separate transaction continues the sequence.
	if err := idx.inTx(ctx, func(tx *sql.Tx) error {
		c, err := nextCSN(ctx, tx)
		if err != nil {
			return err
		}
		got = append(got, c)
		return nil
	}); err != nil {
		t.Fatalf("inTx #2: %v", err)
	}

	want := []uint64{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("nextCSN #%d = %d, want %d", i, got[i], want[i])
		}
	}

	tok, err := readToken(ctx, idx.db)
	if err != nil {
		t.Fatalf("readToken: %v", err)
	}
	if tok != 3 {
		t.Errorf("readToken = %d, want 3 (last issued)", tok)
	}
}

// TestCSN_MarkPrune checks markPrune records the watermark inside the
// transaction.
func TestCSN_MarkPrune(t *testing.T) {
	idx := newMemoryIndex(t)
	ctx := context.Background()

	var issued uint64
	if err := idx.inTx(ctx, func(tx *sql.Tx) error {
		c, err := nextCSN(ctx, tx)
		if err != nil {
			return err
		}
		issued = c
		return markPrune(ctx, tx, c)
	}); err != nil {
		t.Fatalf("inTx: %v", err)
	}

	var prune uint64
	if err := idx.db.QueryRowContext(ctx,
		`SELECT prune_csn FROM index_seq WHERE id = 0`,
	).Scan(&prune); err != nil {
		t.Fatalf("read prune_csn: %v", err)
	}
	if prune != issued {
		t.Errorf("prune_csn = %d, want %d", prune, issued)
	}
}

// TestCSN_RollbackUndoesBump checks an aborted transaction does not advance
// the counter — the stamp shares the write transaction (ADR-106), so a
// rolled-back write leaves Token untouched.
func TestCSN_RollbackUndoesBump(t *testing.T) {
	idx := newMemoryIndex(t)
	ctx := context.Background()

	before, err := readToken(ctx, idx.db)
	if err != nil {
		t.Fatalf("readToken before: %v", err)
	}

	sentinel := errors.New("abort")
	if err := idx.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := nextCSN(ctx, tx); err != nil {
			return err
		}
		return sentinel // force rollback
	}); !errors.Is(err, sentinel) {
		t.Fatalf("inTx: got %v, want sentinel", err)
	}

	after, err := readToken(ctx, idx.db)
	if err != nil {
		t.Fatalf("readToken after: %v", err)
	}
	if after != before {
		t.Errorf("Token advanced across rolled-back tx: before=%d after=%d", before, after)
	}
}
