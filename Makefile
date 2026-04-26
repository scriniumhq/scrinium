# Scrinium — top-level Makefile.
#
# Conventions:
#   make            — same as `make help`
#   make test       — fast race-test, package-level summary
#   make test-v     — verbose, per-test names (use when investigating)
#   make test-pkg P=core
#                   — run tests in one package only; P is a path
#                     suffix under ./
#   make build      — go build ./... (no install)
#   make tidy       — go mod tidy + verify
#   make fmt        — gofmt -s -w on all .go files
#   make vet        — go vet ./...
#   make ci         — fmt-check + vet + test (what CI should run)
#
# We use gotestsum when present for compact output. Falls back to
# plain `go test` if gotestsum is not on $PATH — keeps the Makefile
# usable on a fresh checkout without setup.

GO        ?= go
GOTESTSUM := $(shell command -v gotestsum 2> /dev/null)

# Race detector is on by default. Override with RACE=0 for a faster
# loop on a constrained machine; do not commit with RACE=0 results.
RACE ?= 1
RACE_FLAG := $(if $(filter 1,$(RACE)),-race,)

# Default target.
.DEFAULT_GOAL := help

.PHONY: help
help:
	@echo "Scrinium — make targets:"
	@echo "  build       — go build ./..."
	@echo "  test        — race-test all packages, compact output"
	@echo "  test-v      — same, verbose (per-test names)"
	@echo "  test-pkg P=<pkg-path>  — test one package (e.g. P=core)"
	@echo "  fmt         — gofmt -s -w"
	@echo "  fmt-check   — fail if any file needs gofmt"
	@echo "  vet         — go vet ./..."
	@echo "  tidy        — go mod tidy && go mod verify"
	@echo "  ci          — fmt-check + vet + test"
	@echo "  clean       — remove build artefacts"
	@echo ""
	@echo "Variables:"
	@echo "  RACE=0      — disable race detector (default 1)"

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
# (or P=internal/manifestcodec, P=index/sqlite, etc.)
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
ci: fmt-check vet test

# --- Housekeeping ---

.PHONY: clean
clean:
	$(GO) clean ./...
	rm -f *.test *.out