package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"hash"
	"os"
	"path/filepath"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/index"

	// Side-effect imports register the URI dialers.
	_ "github.com/rkurbatov/scrinium/driver/localfs"
	_ "github.com/rkurbatov/scrinium/index/sqlite"
)

// runInit handles "scrinium-webdav init". It accepts a store
// URI (file:// or bare path), creates the directory if absent,
// then writes the descriptor + system.config via core.InitStore.
// The result is a Plain-DEK store ready to serve — no
// passphrase, no encryption, suitable for local experimentation.
//
// For encrypted stores, hosts use their own initialisation tool
// that invokes core.InitStore with WithPassphrase. The reference
// daemon stays minimal.
//
// Doesn't go through internal/daemon — init is a one-shot
// operation that creates the store before there's anything to
// open. core.OpenStore (which the daemon uses) requires a valid
// system.config; we're producing it.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	storeURI := fs.String("store", "", "Store URI (file:///path or bare /path; required).")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *storeURI == "" {
		fmt.Fprintln(os.Stderr, "scrinium-webdav init: --store is required")
		return 2
	}

	ctx := context.Background()

	// Open driver via the same registry the daemon uses.
	drv, err := driver.DialDriver(*storeURI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: %v\n", err)
		return 1
	}

	// Resolve the store's local path so we can synthesise the
	// default sqlite index URI. init.go can be terse here:
	// only file:// and bare paths are supported (matching the
	// store URI semantics — non-local schemes can't be
	// initialised by us).
	storePath := *storeURI
	if len(storePath) >= 7 && storePath[:7] == "file://" {
		storePath = storePath[7:]
	}
	abs, err := filepath.Abs(storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: resolve path: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: mkdir: %v\n", err)
		return 1
	}

	idx, err := index.DialIndex(ctx, "sqlite:///"+filepath.Join(abs, "index.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: %v\n", err)
		return 1
	}

	store, recoveryKit, err := core.InitStore(ctx, drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(initHashRegistry()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: %v\n", err)
		return 1
	}
	_ = store
	if recoveryKit != nil {
		fmt.Fprintln(os.Stderr, "scrinium-webdav init: unexpected recovery kit produced; refusing to discard")
		return 1
	}

	fmt.Printf("Scrinium store initialised at %s\n", abs)
	fmt.Printf("Run `scrinium-webdav serve --store=file://%s --listen=:8080` to start serving.\n", abs)
	return 0
}

// initHashRegistry returns a HashRegistry with sha256
// registered. Same set as the daemon uses; duplicated here
// rather than reaching into internal/daemon because init runs
// outside the daemon abstraction.
func initHashRegistry() domain.HashRegistry {
	return core.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}
