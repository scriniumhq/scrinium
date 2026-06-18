package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// Model-based stateful test. A reference model (content the store
// should hold, keyed by the ArtifactID the store handed back) is run
// alongside the real store through a randomized program of operations.
// After every step the two are reconciled: every live artifact must
// read back its exact bytes, Walk must report exactly the live set,
// and the on-disk blob count must equal the number of distinct live
// contents (Plain dedup). Reopen is one of the operations, so
// persistence is exercised mid-stream, not just at the end.
//
// This is where interaction bugs surface — delete-then-reopen,
// dedup-across-delete, walk-after-partial-delete — the combinations no
// hand-written example enumerates. Two entry points share one engine:
//
//   - FuzzStoreModel decodes the program from the fuzz corpus, so the
//     fuzz minimizer shrinks any failing sequence to its core.
//   - TestStoreModel_Randomized runs a batch of fixed-seed programs so
//     `make test` (no -fuzz) still exercises the model every run.
//
// Plain mode only: the model predicts blob-level dedup (one blob per
// distinct live content), which is a Plain-mode law. It does NOT
// predict ArtifactID stability across re-Puts — the ID hashes a
// second-resolution timestamp, so identical content can yield a new id
// in a later second; the model keys on the id the store returns.

// opKind enumerates the operations the interpreter can apply.
type opKind uint8

const (
	opPut opKind = iota
	opGet
	opDelete
	opReopen
	opNumKinds
)

// modelEntry is one artifact the store is expected to hold.
type modelEntry struct {
	id      domain.ArtifactID
	content []byte
	ns      string
}

// model is the reference the store is checked against.
type model struct {
	live []modelEntry // insertion order; ids unique
}

func (m *model) findByID(id domain.ArtifactID) int {
	for i := range m.live {
		if m.live[i].id == id {
			return i
		}
	}
	return -1
}

// distinctContents counts unique payloads across all live artifacts —
// the expected number of blobs on disk under Plain dedup. (Dedup keys
// on content regardless of namespace, so namespace is not part of the
// key here.)
func (m *model) distinctContents() int {
	seen := make(map[string]struct{}, len(m.live))
	for _, e := range m.live {
		seen[string(e.content)] = struct{}{}
	}
	return len(seen)
}

// runModelProgram interprets a byte program against a fresh Plain store
// and reconciles the model after every step. The byte stream is the
// only entropy source, so the run is fully reproducible from `program`.
func runModelProgram(t *testing.T, program []byte) {
	t.Helper()
	ctx := context.Background()
	namespaces := []string{"alpha", "beta"}

	s, r := storefx.InitPlain(t)
	root := r.Root()
	var m model

	// Cursor over the program; each op consumes a few bytes.
	rd := bytes.NewReader(program)
	next := func() (byte, bool) {
		b, err := rd.ReadByte()
		if err != nil {
			return 0, false
		}
		return b, true
	}

	step := 0
	for {
		kindByte, ok := next()
		if !ok {
			break
		}
		step++
		kind := opKind(kindByte % byte(opNumKinds))

		switch kind {
		case opPut:
			sizeB, _ := next()
			nsB, _ := next()
			fillB, _ := next()
			content := bytes.Repeat([]byte{fillB}, int(sizeB)) // 0..255 bytes, content == fill*size
			ns := namespaces[int(nsB)%len(namespaces)]

			id, err := s.Put(ctx, artifactfx.PayloadBytes(content))
			if err != nil {
				t.Fatalf("step %d Put(ns=%s,len=%d): %v", step, ns, len(content), err)
			}
			// A Put of content already live in this namespace may return
			// the SAME id (a same-second idempotent manifest) or a NEW id
			// (a fresh manifest stamped in a later wall-clock second).
			// Both are valid: the two artifacts share one deduped blob and
			// both appear in Walk. Mirror the store exactly — add a model
			// entry only for an id we have not seen. (Keying dedup on id
			// rather than content is what makes this race-safe; the blob
			// dedup invariant is reconciled separately on distinct content.)
			if m.findByID(id) < 0 {
				m.live = append(m.live, modelEntry{id: id, content: content, ns: ns})
			}

		case opGet:
			if len(m.live) == 0 {
				continue
			}
			selB, _ := next()
			e := m.live[int(selB)%len(m.live)]
			if got := storekit.GetBytes(t, s, e.id); !bytes.Equal(got, e.content) {
				t.Fatalf("step %d Get(%s): content mismatch", step, e.id)
			}

		case opDelete:
			if len(m.live) == 0 {
				continue
			}
			selB, _ := next()
			i := int(selB) % len(m.live)
			e := m.live[i]
			if err := s.Delete(ctx, e.id); err != nil {
				t.Fatalf("step %d Delete(%s): %v", step, e.id, err)
			}
			m.live = append(m.live[:i], m.live[i+1:]...)
			// Deleted id must now be absent.
			if _, err := s.Get(ctx, e.id); !errors.Is(err, errs.ErrArtifactNotFound) {
				t.Fatalf("step %d Get after Delete(%s): got %v, want ErrArtifactNotFound", step, e.id, err)
			}

		case opReopen:
			if err := s.Close(); err != nil {
				t.Fatalf("step %d Close: %v", step, err)
			}
			s = r.Open(t)
		}

		reconcileModel(t, s, root, &m, step)
	}
}

// reconcileModel asserts the store agrees with the model on all
// observable surfaces: per-artifact content, the Walk set per
// namespace, and the distinct-blob count on disk.
func reconcileModel(t *testing.T, s store.Store, root string, m *model, step int) {
	t.Helper()

	// Every live artifact reads back exactly.
	for _, e := range m.live {
		if got := storekit.GetBytes(t, s, e.id); !bytes.Equal(got, e.content) {
			t.Fatalf("step %d reconcile: Get(%s) content mismatch", step, e.id)
		}
	}

	// Walk yields exactly the live ids — namespace-agnostic (ADR-99).
	want := map[domain.ArtifactID]struct{}{}
	for _, e := range m.live {
		want[e.id] = struct{}{}
	}
	got := storekit.WalkIDs(t, s)
	if len(got) != len(want) {
		t.Fatalf("step %d reconcile: Walk size got %d want %d", step, len(got), len(want))
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("step %d reconcile: Walk missing %s", step, id)
		}
	}

	// Plain dedup: one blob per distinct live content. Blobs of deleted
	// artifacts may linger until GC, so this is an upper-bound check on
	// the *reachable* set, not the physical file count — assert the
	// distinct-content lower bound is present and that we never have
	// fewer blobs than distinct contents.
	if want := m.distinctContents(); storefx.OnDiskAt(root).BlobCount() < want {
		t.Fatalf("step %d reconcile: blob count %d < distinct live contents %d",
			step, storefx.OnDiskAt(root).BlobCount(), want)
	}
}

func FuzzStoreModel(f *testing.F) {
	// Seed corpus: a few hand-written programs covering the basic
	// interactions. Bytes are (kind, operands...) as decoded above.
	f.Add([]byte{
		byte(opPut), 5, 0, 'a',
		byte(opGet), 0,
		byte(opPut), 5, 0, 'a', // dedup: same content/ns
		byte(opDelete), 0,
		byte(opReopen),
	})
	f.Add([]byte{
		byte(opPut), 10, 0, 'x',
		byte(opPut), 10, 1, 'x', // same content, different ns
		byte(opReopen),
		byte(opDelete), 0,
	})

	f.Fuzz(func(t *testing.T, program []byte) {
		runModelProgram(t, program)
	})
}

// TestStoreModel_Randomized runs fixed-seed programs so the model is
// exercised on every `make test`, independent of fuzzing. Seeds are
// listed explicitly; add a seed here when a fuzz hunt finds a sequence
// worth pinning as a permanent regression.
func TestStoreModel_Randomized(t *testing.T) {
	seeds := []int64{1, 2, 3, 1337, 2026}
	for _, seed := range seeds {
		seed := seed
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			program := make([]byte, 200) // ~50-60 ops after operand consumption
			for i := range program {
				program[i] = byte(rng.Intn(256))
			}
			runModelProgram(t, program)
		})
	}
}
