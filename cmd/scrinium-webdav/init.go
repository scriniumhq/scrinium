package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"scrinium.dev"
)

// runInit handles "scrinium-webdav init". It accepts a store
// URI (file:// or bare path) and an optional passphrase file,
// then delegates to scrinium.Init which creates the directory,
// writes the descriptor + system.config, and returns a runtime
// already open for use.
//
// init is one-shot: we Init, write any recovery kit to stderr,
// and Close. Hosts that need different lifecycle semantics
// (init-then-serve, init with custom HashRegistry, etc.) build
// their own using scrinium.Init directly.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	storeURI := fs.String("store", "", "Store URI (file:///path or bare /path; required).")
	passphraseFile := fs.String("passphrase-file", "", "Path to a file containing the store passphrase. Empty = Plain (unencrypted).")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *storeURI == "" {
		fmt.Fprintln(os.Stderr, "scrinium-webdav init: --store is required")
		return 2
	}

	ctx := context.Background()

	cfg := scrinium.DefaultConfig()
	cfg.Store = *storeURI
	cfg.PassphraseFile = *passphraseFile

	s, kit, err := scrinium.Init(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav init: %v\n", err)
		return 1
	}
	// Always Close — wipes secrets, closes index handle.
	defer func() {
		if err := s.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium-webdav init: close: %v\n", err)
		}
	}()

	if kit != nil {
		// Encrypted Init: emit the recovery kit to stderr so
		// the user can capture it via shell redirection. Hosts
		// integrating this into a UI should pipe stderr to the
		// place they want the kit displayed/persisted.
		fmt.Fprintln(os.Stderr, "--- BEGIN SCRINIUM RECOVERY KIT ---")
		os.Stderr.Write(kit)
		fmt.Fprintln(os.Stderr, "--- END SCRINIUM RECOVERY KIT ---")
		fmt.Fprintln(os.Stderr, "Store this kit somewhere safe. It is the only path to recover")
		fmt.Fprintln(os.Stderr, "the store if the passphrase is lost.")
	}

	fmt.Printf("Scrinium store initialised at %s\n", *storeURI)
	if *passphraseFile != "" {
		fmt.Printf("Run `scrinium-webdav serve --store=%s --passphrase-file=%s --listen=:8080` to start serving.\n", *storeURI, *passphraseFile)
	} else {
		fmt.Printf("Run `scrinium-webdav serve --store=%s --listen=:8080` to start serving.\n", *storeURI)
	}
	return 0
}
