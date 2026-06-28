# Testing strategy

This document is the placement guide for tests in Scrinium. Its job is
to stop tests accreting ad hoc. Before adding a test, identify which
**category** the thing under test falls into, then use the technique
this document assigns to that category. If a test does not fit a
category, that is a signal to reconsider what it is actually asserting —
not to invent a new style.

The guiding principle is **test contracts and invariants, not code
paths**. A test that pins a specific implementation mechanic (an
on-disk byte layout, an internal call order) breaks on refactor and
teaches nothing about correctness. A test that pins a contract survives
reimplementation — which is the whole point, since the implementation
will keep changing.

We do not chase a test count. A handful of strong, general tests beats
hundreds of example tests that each pin one input/output tuple.

## Categories and techniques

### 1. Parsers and decoders → fuzz

Any function that turns untrusted or adversarial bytes into structure.
These are the genuine fuzz targets: the input space is bytes, malformed
input must never panic / hang / produce a "valid" wrong result, and the
fuzz minimizer shrinks failures to something debuggable.

Examples in-tree: `artifact.Decode`, `artifact.ReadHeader`,
`descriptor.Unmarshal`. `descriptor.FuzzUnmarshal` is the reference
shape — rich seed corpus, contract "never panic, never return an
invalid value, reparse is stable".

Should also be fuzzed but currently are not (open work): the `zstd` and
`aesgcm` stage decoders, `segaead.Open` (truncated / tampered
segments), `fsmeta.Decode`, `path.Parse`. Decompression and decryption
of attacker-influenced bytes is the highest-value place to fuzz.

**Do not fuzz opaque round-trips.** `Get(Put(x)) == x` over a payload
the engine only stores (never parses) gains nothing from byte-level
mutation; the meaningful axis is size, not content. Use a seeded
property driver (category 4) instead. Wrapping such a property in a
`Fuzz*` target is cargo-cult — the seed corpus is the only thing that
ever runs in the default cycle.

### 2. Pure functions with algebraic laws → table / property

Deterministic functions whose correctness is a law: hashing,
content-hash formatting, descriptor equality, checksums, path
canonicalization. Use focused table tests for the enumerable cases and
a seeded property driver for the law (e.g. idempotence, symmetry,
round-trip). Fuzz only if the function also parses bytes.

### 3. Interface contracts → conformance suite

When an interface has, or will have, more than one implementation, the
contract is tested once and run against each implementation through a
shared suite. `internal/testutil/indexsuite` is the model:
`indexsuite.Run(t, Factory)` exercises the `StoreIndex` contract
black-box and is invoked from the sqlite package (and will be from a
future postgres backend).

Rule: a conformance suite lives in `internal/testutil` **only when 2+
implementations exist**. With a single implementation the `Factory`
indirection buys nothing and hides the test from the code it covers —
write a plain `*_test.go` in the implementation's package (external
`_test` package keeps it black-box). Promote to a shared suite the
moment a second real implementation lands. (This is why the SystemStore
conformance suite was collapsed back into `engine/store`.)

The `Driver` interface is a conformance candidate, but a `drivertest`
suite is **deliberately deferred until a second real implementation
lands** (s3 is currently a stub, exactly like the postgres index). The
reasoning is the same one that made us collapse the SystemStore suite:
with a single implementation, a `Factory`-indirected suite is an
abstraction ahead of need — it hides the tests from the code they cover
and proves nothing beyond the direct tests. There is no shared
high-level driver layer to test once and reuse; `engine/driver` is just
the interface plus DTOs plus the dialer registry, and `localfs`
implements all 17 methods itself. So the portable contract currently
lives where it belongs: as direct tests in `engine/driver/localfs`
(~34 of them — round-trip, not-found, ReadAt ranges, rename, clone,
list, tombstone, etc.).

These localfs tests are already written portably (assert
`errors.Is(err, fs.ErrNotExist)` rather than an OS-specific error;
compare `List` results as a set, not an ordered slice), so that on the
day s3 becomes real, the move is mechanical: lift the backend-agnostic
cases into `internal/testutil/drivertest`, parameterise them over a
`Factory`, and run the suite against both drivers. Backend-specific
behaviour stays put — localfs path-safety/traversal, `CreatesMissingRoot`,
`PruneEmptyDirs` (POSIX directories; s3 has none), `Open` URI schemes,
and faulty's failure-rate/latency tests are not part of the portable
contract and do not move.

### 4. Stateful components → model-based + crash-consistency

The store and the agent workers (gc, scrub, ingester, ejector) are
stateful. Two complementary techniques:

**Model-based.** Run a reference model alongside the real component
through a randomized program of operations; reconcile after every step.
`engine/store/store_model_test.go` is the template: a `model`, an
operation interpreter, and a `reconcileModel` that checks content, the
`Walk` set, and on-disk blob count. Driven two ways — a `Fuzz*` target
(the program is decoded from the fuzz bytes, so mutation explores
operation interleavings and the minimizer shrinks failing programs:
this **is** a legitimate fuzz use) and a fixed-seed `Test*` that runs on
every `make test`.

**Crash-consistency.** The engine's atomicity claims are tested by
injecting faults and recovering. Use the `faulty` driver
(`driverfx.Faulty`) and its deterministic `SetFailOnCall(method, n)` to
fail the k-th I/O write of an operation, for every k across the
operation's write window, then reopen cleanly and assert the result is
either fully present and byte-identical or fully absent — never torn.
`engine/store/store_crash_test.go` is the template. This is what makes
a storage engine trustworthy under power loss; it is not optional for
mutating operations.

### 5. Surfaces and integration → thin tests on shared fixtures

The reference binaries (`cmd/scrinium-fuse`, `-webdav`, `-webview`) and
the `examples/` programs are adapters over the engine. Test them
through the shared projection stack, not by re-wiring it per file. Use
`viewfx.Stack` (View + FSOps over an in-memory `FakeSource`) and
`viewfx.RoutingAll`. Surface tests assert adapter-specific behaviour
(errno mapping, path cleaning, WebDAV semantics) — not engine behaviour,
which is already covered by categories 3–4.

### 6. Genuinely enumerable facts → small table tests

Error-to-errno / sentinel mappings, state guards (operation blocked in
ReadOnly / Offline / by policy), input validation (rejected namespaces,
oversized fields). These are finite and example-shaped by nature. One
table test per family, not one top-level function per case.

## Fixtures (`internal/testutil`)

One fixture concern per package. Constructors take `testing.TB`,
register their own `t.Cleanup`, and `t.Fatalf` on setup failure.

- `driverfx` — `LocalFS` (localfs in a tempdir), `Faulty` (fault
  injection wrapper).
- `indexfx` — `Memory`, `Disk` (sqlite-backed `StoreIndex`).
- `eventfx` — `Recorder`, a concurrency-safe `EventBus` capture for
  asserting what a component published.
- `artifactfx`, `manifestfx` — domain object builders (manifests,
  synthetic hashes, DEKs, encoded artifact bytes).
- `storefx` — store lifecycle: `Init` / `InitWithRoot` / `InitOn` /
  `InitPlain` / `InitEncrypted`, the `Reopener` (init → close → reopen),
  `OnDisk` (physical inspection), and the passphrase providers
  (`StaticPP` / `ScriptedPP` / `RecordingPP`).
- `projectionfx` — `FakeSource` / `FakeReadHandle`, in-memory fakes of
  the projection-facing interfaces. **Must not import `projection`**: it
  satisfies the interfaces structurally so even the projection package's
  own tests can use it. Keep it dependency-light.
- `viewfx` — the consumer layer that wires `FakeSource` into a real
  `View` + `FSOps` (`Stack`, `RoutingAll`). Imports `projection`; that
  is why it is separate from `projectionfx`.
- `indexsuite` — the `StoreIndex` conformance suite (category 3).

When the setup for a category-4 test is elaborate and reused (e.g. a
store with a blob-encrypting pipeline: `StoreConfig.Pipeline =
["aes-gcm"]` plus a registered transformer and a `KeyResolver`), that
boilerplate belongs in a fixture, not inlined per test.

## Crypto: two orthogonal axes

A recurring source of false assumptions, so stated explicitly:

- **`ManifestCrypto`** (`Sealed` / `Paranoid`) encrypts **manifest
  fields** — `usr`/`ext`, and `Namespace` under Paranoid. It does **not**
  encrypt the blob payload. Assert it on the raw manifest file bytes.
- **`StoreConfig.Pipeline`** (e.g. `["aes-gcm"]`) encrypts the **blob
  payload**, and requires a registered transformer and a `KeyResolver`.
  Assert blob-at-rest on the raw blob bytes.

`storefx.InitEncrypted` sets up the first axis only. Do not assert
blob-at-rest on a store configured with `ManifestCrypto` alone.

Encrypted blobs use a random IV, so identical plaintext yields distinct
ciphertext and distinct blobs **by design** — content-addressing and
dedup laws (category 4) hold in **Plain mode only**.

## The Fuzz + seeded pattern

Every property has its body in a shared `check*(t, input)` helper, called
by both:

- a `Fuzz*` target — active hunting (`make fuzz`) and seed-corpus smoke
  (`make fuzz-smoke`, in `make ci`);
- a `Test*_Seeded` driver — the same law over a broad, deterministic
  spread of inputs (fixed RNG seed), so the property has real coverage
  on every `make test` without an active `-fuzz` run.

Without the seeded driver, a `Fuzz*` property only ever sees its
hand-written seed corpus in the default cycle — a few examples, not a
property. The seeded driver is mandatory for any property whose `Fuzz*`
target is the only other driver. (For category-1 parser fuzz, the seed
corpus plus `make fuzz-smoke` is sufficient; no seeded driver needed.)

Active fuzzing is not in the required cycle — run it manually or as a
nightly CI job (`go test -fuzz=Name -fuzztime=...`). Crashes land in
`testdata/fuzz/` and are committed as permanent regression seeds.

## Running

See the Makefile. `make test` (fast, no race), `make test-pkg P=<pkg>`,
`RACE=1 make test` (concurrency-focused), `make fuzz-smoke`,
`make fuzz P=<pkg> F=<Fuzz>`, `make smoke` (million-files scale). CI runs
`make ci` = fmt-check + vet + test + fuzz-smoke; a separate job runs
`RACE=1 make test`.

## Checklist for a new test

1. Which category (1–6) does the thing under test fall into?
2. Use that category's technique. If it does not fit, reconsider what
   you are asserting.
3. Is it a contract or a mechanic? Prefer the contract.
4. Reuse a fixture; do not re-wire setup inline. Missing fixture →
   add it to `internal/testutil`.
5. Property? Add the `check*` helper + `Test*_Seeded`, not just `Fuzz*`.
6. Mutating store operation? It needs crash-consistency coverage.
7. Could this collapse several existing example tests? If so, do it and
   delete them.