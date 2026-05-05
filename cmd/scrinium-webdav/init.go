package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/driver/localfs"
	"github.com/rkurbatov/scrinium/index/sqlite"
)

// runInit handles "scrinium-webdav init". It creates the store
// directory if absent and writes the descriptor + system.config
// via core.InitStore. The result is a Plain-DEK store ready to
// serve — no passphrase, no encryption, suitable for local
// experimentation.
//
// For encrypted stores, hosts use their own initialisation tool
// that invokes core.InitStore with WithPassphrase. The reference
// daemon stays minimal.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	storePath := fs.String("store-path", "", "Path where the store will be created (required).")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *storePath == "" {
		fmt.Fprintln(os.Stderr, "scrinium-webdav init: --store-path is required")
		return 2
	}

	abs, err := filepath.Abs(*storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: resolve path: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: mkdir: %v\n", err)
		return 1
	}

	ctx := context.Background()

	drv, err := localfs.New(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: localfs driver: %v\n", err)
		return 1
	}
	idx, err := sqlite.NewStore(ctx, filepath.Join(abs, "index.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: sqlite index: %v\n", err)
		return 1
	}

	store, recoveryKit, err := core.InitStore(ctx, drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(defaultHashRegistry()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: %v\n", err)
		return 1
	}
	_ = store
	if recoveryKit != nil {
		// Plain-DEK init returns nil; if a kit is produced, the
		// daemon is misconfigured (it never asks for a passphrase).
		fmt.Fprintln(os.Stderr, "scrinium-webdav init: unexpected recovery kit produced; refusing to discard")
		return 1
	}

	fmt.Printf("Scrinium store initialised at %s\n", abs)
	fmt.Println("Run `scrinium-webdav serve --store-path=" + abs + " --listen=:8080` to start serving.")
	return 0
}
