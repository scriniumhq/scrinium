# Scrinium — top-level Makefile.
#
# Single Go module (scrinium.dev). Source layout:
#   /            — high-level wrapper API (scrinium.Open / scrinium.Init)
#   /store/...   — engine internals (store, domain, driver, index, ...)
#   /cmd/...     — reference binaries (scrinium-fuse, scrinium-webdav, scrinium-webview)
#   /examples/...— small runnable programs (hello, ingest, browse)
#
# `go build ./...` and `go test ./...` operate across the whole tree.
#
# Conventions:
#   make            — same as `make help`
#   make test       — fast test, package-level summary, no race
#   make test-v     — verbose, per-test names (use when investigating)
#   make test-pkg P=core
#                   — run tests in one package only; P is a path
#                     suffix under ./
#   make smoke      — long-running million-files smoke (no race,
#                     bypasses gotestsum for live stderr progress);
#                     N=K to override the artifact count
#   make build      — go build ./... (no install)
#   make tidy       — go mod tidy && go mod verify
#   make fmt        — gofmt -s -w on all .go files
#   make vet        — go vet ./...
#   make ci         — fmt-check + vet + test (what CI should run)
#
# We use gotestsum when present for compact output. Falls back to
# plain `go test` if gotestsum is not on $PATH — keeps the Makefile
# usable on a fresh checkout without setup.

GO        ?= go
GOTESTSUM := $(shell command -v gotestsum 2> /dev/null)

# Race detector OFF by default — most tests are single-threaded
# and the detector adds 5-10x overhead. Turn it on explicitly for
# concurrency-focused runs and CI:  RACE=1 make test
RACE ?= 0
RACE_FLAG := $(if $(filter 1,$(RACE)),-race,)

# Default fuzz duration for `make fuzz`. Override with T=2m or T=1h
# for longer hunts. The smoke target ignores this — it only runs
# the seed corpus, which takes seconds.
FUZZTIME ?= 30s

# Benchmark knobs (see the Benchmarks section for details).
BENCH      ?= .
BENCHPKG   ?= ./store/artifact/ ./store/
BENCHCOUNT ?= 10

# Default target.
.DEFAULT_GOAL := help

.PHONY: help
help:
	@echo "Scrinium — make targets:"
	@echo "  build       — go build ./..."
	@echo "  test        — test all packages, compact output (no race)"
	@echo "  test-v      — same, verbose (per-test names)"
	@echo "  test-pkg P=<pkg-path>  — test one package (e.g. P=core)"
	@echo "  smoke [N=K] — long-running million-files M1 smoke;"
	@echo "                always without -race; default N=1_000_000"
	@echo "  fmt         — gofmt -s -w"
	@echo "  fmt-check   — fail if any file needs gofmt"
	@echo "  vet         — go vet ./..."
	@echo "  tidy        — go mod tidy && go mod verify"
	@echo "  fuzz-smoke  — seed-corpus pass over every Fuzz* (CI-fast)"
	@echo "  fuzz        — active fuzzing of one target;"
	@echo "                P=<pkg> F=<FuzzName> [T=<duration>]"
	@echo "  fuzz-list   — list every Fuzz* in the tree with its package"
	@echo "  fuzz-all    — active-fuzz every target, FUZZTIME each (nightly)"
	@echo "  fuzz-clean  — remove generated fuzz corpora (testdata/fuzz/)"
	@echo "  bench       — run benchmarks (-benchmem -count=$(BENCHCOUNT)); BENCH=, BENCHPKG="
	@echo "  bench-cmp   — run + benchstat diff vs committed bench-baseline.txt"
	@echo "  bench-baseline — refresh bench-baseline.txt from a fresh run"
	@echo "  ci          — fmt-check + vet + test + fuzz-smoke"
	@echo "  clean       — remove build artefacts (also runs fuzz-clean)"
	@echo ""
	@echo "Variables:"
	@echo "  RACE=1      — enable race detector (default 0)"
	@echo "  FUZZTIME=2m — duration for active fuzz (default 30s)"

# --- Build ---

.PHONY: build
build:
	$(GO) build ./...

# --- Tests ---
#
# We split test, test-v, test-pkg into separate targets rather than
# one parameterised target because the tail of `go test` is order-
# dependent (flags before pkgs vs after differ slightly with
# gotestsum's `--`). Three short targets are clearer than one with
# conditional shell glue.

.PHONY: test
test:
ifdef GOTESTSUM
	$(GOTESTSUM) --format pkgname -- $(RACE_FLAG) ./...
else
	$(GO) test $(RACE_FLAG) ./...
endif

.PHONY: test-v
test-v:
ifdef GOTESTSUM
	$(GOTESTSUM) --format testname -- $(RACE_FLAG) ./...
else
	$(GO) test -v $(RACE_FLAG) ./...
endif

# Single-package test. Usage: make test-pkg P=core
# (or P=store/internal/artifactio, P=store/index/sqlite, etc.)
.PHONY: test-pkg
test-pkg:
ifndef P
	@echo "Usage: make test-pkg P=<package-path>"; exit 1
endif
ifdef GOTESTSUM
	$(GOTESTSUM) --format testname -- $(RACE_FLAG) -v ./$(P)/...
else
	$(GO) test -v $(RACE_FLAG) ./$(P)/...
endif

# Long-running smoke: million-files round-trip from the M1 exit
# criteria. Always without -race (single-threaded path; race
# detector adds 10x overhead for nothing) and without gotestsum
# (we want live stderr progress, not a buffered summary).
# Override N for quicker triage runs:
#   make smoke N=10000      ~5-10s, sanity check
#   make smoke              default 100k, ~1-2min, M1 O(1)-memory proof
#   make smoke N=1000000    full 1M, ~12-15min, literal spec target
#                            (run before tagging a release)
#
# Disk vs ramdisk. Default uses $TMPDIR on the system disk: APFS
# (macOS) and ext4 (Linux) handle a million small files at roughly
# 800-1500 Put/s on NVMe. The smoke is IOPS-bound, not bandwidth-
# bound — small files spend most of their time in directory ops
# and fsync (which is off, but the kernel still serialises).
#
# Ramdisk gives a 5-10x speedup and avoids wearing the SSD on
# repeated runs. Setup is one-shot per session:
#
#   macOS (4 GiB ramdisk under /Volumes/scrinium-ram):
#     diskutil erasevolume APFS scrinium-ram \
#         $$(hdiutil attach -nomount ram://8388608)
#     TMPDIR=/Volumes/scrinium-ram make smoke N=1000000
#     diskutil eject /Volumes/scrinium-ram        # cleanup; one shot
#
#   Linux (4 GiB tmpfs under /mnt/scrinium-ram):
#     sudo mkdir -p /mnt/scrinium-ram
#     sudo mount -t tmpfs -o size=4G tmpfs /mnt/scrinium-ram
#     sudo chown $$USER /mnt/scrinium-ram
#     TMPDIR=/mnt/scrinium-ram make smoke N=1000000
#     sudo umount /mnt/scrinium-ram               # cleanup; one shot
#
# Ctrl-C during a smoke leaves the partial tree behind (Go's
# t.TempDir cleanup runs on test completion, not on signal).
# On ramdisk: eject/umount nukes everything in one operation.
# On regular $TMPDIR: find $TMPDIR -maxdepth 1 -name 'TestSmoke_*'
# -exec rm -rf {} +
.PHONY: smoke
smoke:
ifdef N
	SCRINIUM_SMOKE=1 SCRINIUM_SMOKE_N=$(N) $(GO) test -v -timeout 30m -count=1 -run TestSmoke_MillionSmallFiles ./engine/store/
else
	SCRINIUM_SMOKE=1 $(GO) test -v -timeout 30m -count=1 -run TestSmoke_MillionSmallFiles ./engine/store/
endif

# Encrypted smoke: round-trip on a Store with Paranoid manifests.
# Smaller default N than `make smoke` (10k vs 100k) — encrypted
# Put adds AES-GCM overhead per manifest, so the run-time-per-
# artifact is meaningfully higher; 10k is enough to demonstrate
# stability without dragging the local feedback loop. Override
# with N=... for stress runs.
.PHONY: smoke-encrypted
smoke-encrypted:
ifdef N
	SCRINIUM_SMOKE_ENCRYPTED=1 SCRINIUM_SMOKE_N=$(N) $(GO) test -v -timeout 30m -count=1 -run TestSmoke_EncryptedRoundTrip ./store/
else
	SCRINIUM_SMOKE_ENCRYPTED=1 $(GO) test -v -timeout 30m -count=1 -run TestSmoke_EncryptedRoundTrip ./store/
endif

# --- Fuzzing ---
#
# Two distinct flows:
#
# 1. fuzz-smoke — `go test -run=^Fuzz` runs every Fuzz* function
#    against its seed corpus only, no mutation. Takes seconds and
#    catches the most common regression: a Fuzz* that no longer
#    compiles, or a seed that newly panics after a refactor.
#    Wired into `make ci` so a broken fuzz target fails the same
#    check that runs unit tests.
#
# 2. fuzz — active fuzzing of ONE target for FUZZTIME (default
#    30s). `go test -fuzz=...` accepts one regex per invocation;
#    we expose this as `make fuzz P=<pkg> F=<FuzzName>`.
#    Discovered crashes land in the package's
#    testdata/fuzz/<FuzzName>/ directory; commit them as
#    permanent regression seeds.

.PHONY: fuzz-smoke
fuzz-smoke:
ifdef GOTESTSUM
	$(GOTESTSUM) --format pkgname -- -run=^Fuzz $(RACE_FLAG) ./...
else
	$(GO) test -run=^Fuzz $(RACE_FLAG) ./...
endif

# Active fuzz. Usage:
#   make fuzz P=store/internal/descriptor F=FuzzUnmarshal
#   make fuzz P=store/internal/artifactio F=FuzzDecodeFile T=2m
.PHONY: fuzz
fuzz:
ifndef P
	@echo "Usage: make fuzz P=<package-path> F=<FuzzName> [T=<duration>]"
	@echo ""
	@$(MAKE) --no-print-directory fuzz-list
	@exit 1
endif
ifndef F
	@echo "Usage: make fuzz P=$(P) F=<FuzzName> [T=<duration>]"
	@echo ""
	@echo "Fuzz targets in ./$(P):"
	@grep -hoE '^func (Fuzz[A-Za-z0-9_]+)' ./$(P)/*_test.go 2>/dev/null \
	    | awk '{print "  "$$2}' | sort -u
	@exit 1
endif
	$(GO) test -run=^$$ -fuzz=^$(F)$$ -fuzztime=$(FUZZTIME) ./$(P)/...

# Inventory of every Fuzz* across the tree, with the package it
# lives in. `git grep` keeps the listing fast; falls back to grep
# if the working tree is not a git checkout.
.PHONY: fuzz-list
fuzz-list:
	@echo "Fuzz targets:"
	@if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then \
	    git grep -hnE '^func (Fuzz[A-Za-z0-9_]+)' -- '*_test.go' \
	        | awk -F: '{ \
	              file=$$1; \
	              sub(/\/[^\/]+$$/, "", file); \
	              match($$0, /Fuzz[A-Za-z0-9_]+/); \
	              name=substr($$0, RSTART, RLENGTH); \
	              print "  P="file" F="name; \
	          }' | sort -u; \
	else \
	    grep -rhnE '^func (Fuzz[A-Za-z0-9_]+)' --include='*_test.go' . \
	        | awk -F: '{ \
	              file=$$1; sub(/^\.\//, "", file); \
	              sub(/\/[^\/]+$$/, "", file); \
	              match($$0, /Fuzz[A-Za-z0-9_]+/); \
	              name=substr($$0, RSTART, RLENGTH); \
	              print "  P="file" F="name; \
	          }' | sort -u; \
	fi

# fuzz-all — active-fuzz EVERY target for FUZZTIME each, sequentially.
# Go's -fuzz takes one target in one package per invocation, so there is
# no single-command way to fuzz the whole tree; this loops the inventory.
# Total wall time ≈ (number of targets) × FUZZTIME. Stops at the first
# crash (seed written under testdata/fuzz/). Nightly/manual — never `make test`.
.PHONY: fuzz-all
fuzz-all:
	@set -e; \
	for pkg in $$($(GO) list ./...); do \
	  for fn in $$($(GO) test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz' || true); do \
	    echo "==> $$pkg $$fn (fuzztime=$(FUZZTIME))"; \
	    $(GO) test -run=^$$ -fuzz=^$$fn$$ -fuzztime=$(FUZZTIME) $$pkg; \
	  done; \
	done

# Wipe generated fuzz corpora. testdata/fuzz/ accumulates between
# active runs; trim it before commits to keep `git status` clean.
# Manually-curated regression seeds (committed under
# testdata/fuzz/<Name>/) are NOT touched — only files that match
# Go's auto-generated naming.
.PHONY: fuzz-clean
fuzz-clean:
	@find . -type d -name fuzz -path '*/testdata/*' | while read d; do \
	    find "$$d" -mindepth 2 -type f ! -name 'README*' -print -delete 2>/dev/null; \
	done

# --- Benchmarks ---
# Micro-benchmarks track CPU/allocation cost of hot paths (manifest
# codec, Put). NOT part of `make test`/`ci`: benchmarks are slow and
# sec/op is machine-dependent, so CI cannot gate on them honestly. The
# durable, machine-independent signal is B/op + allocs/op; the committed
# reference lives in the benchmark files' header comments and in
# bench-baseline.txt.
#
# BENCHPKG lists the packages that actually contain benchmarks — NOT
# ./..., which scans the whole tree and would (a) waste time building
# every package and (b) interleave per-package status lines into the
# saved file. The recipe also greps the output down to benchstat-
# relevant lines, so bench-new.txt stays clean regardless. Add packages
# to BENCHPKG as new benchmarks appear.
#
# Usage:
#   make bench                  # run -> bench-new.txt (+ full log)
#   make bench BENCH=Manifest   # restrict to matching benchmark names
#   make bench BENCHPKG=./store/artifact/   # one package
#   make bench-cmp              # run + benchstat diff vs bench-baseline.txt
#   make bench-baseline         # seed/refresh bench-baseline.txt from a run
#
# benchstat reads only Benchmark/goos/goarch/pkg/cpu lines, so the grep
# filter below produces exactly what it needs. The full run (including
# any FAIL) is streamed live and saved to bench-full.txt for debugging.

.PHONY: bench
bench:
	@$(GO) test -run=^$$ -bench=$(BENCH) -benchmem -count=$(BENCHCOUNT) $(BENCHPKG) | tee bench-full.txt
	@grep -E '^(goos:|goarch:|pkg:|cpu:|Benchmark)' bench-full.txt > bench-new.txt || \
	  { echo "no benchmark output captured — did the run fail? see bench-full.txt"; exit 1; }
	@echo "-> bench-new.txt (clean benchstat input); full log: bench-full.txt"

.PHONY: bench-cmp
bench-cmp: bench
	@command -v benchstat >/dev/null 2>&1 || { \
	  echo "benchstat not found: go install golang.org/x/perf/cmd/benchstat@latest"; exit 1; }
	@test -f bench-baseline.txt || { \
	  echo "no bench-baseline.txt — run 'make bench-baseline' first"; exit 1; }
	benchstat bench-baseline.txt bench-new.txt

.PHONY: bench-baseline
bench-baseline: bench
	@cp bench-new.txt bench-baseline.txt
	@echo "baseline saved to bench-baseline.txt — commit it"

# --- Quality gates ---

.PHONY: fmt
fmt:
	gofmt -s -w .

.PHONY: fmt-check
fmt-check:
	@out=$$(gofmt -s -l .); \
	if [ -n "$$out" ]; then \
	  echo "gofmt needs to run on:"; \
	  echo "$$out"; \
	  exit 1; \
	fi

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: tidy
tidy:
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: ci
ci: fmt-check vet test fuzz-smoke

# --- Housekeeping ---
#
# bench-baseline.txt is a committed reference and is NOT removed here.
# bench-new.txt / bench-full.txt are transient — add them to .gitignore.
.PHONY: clean
clean: fuzz-clean
	$(GO) clean ./...
	rm -f *.test *.out bench-new.txt bench-full.txt