// hello is the smallest Scrinium program: it assembles (creating if
// needed) a store from an inline composer config, puts one artifact,
// reads it back, and closes. About 100 lines, all of which serve a
// purpose — copy it as a starting point for your own integration.
//
// It shows the modern entry point: describe the store declaratively
// (a composer YAML document) and let composer.LoadOrInitYAML assemble
// it, then work with the returned assembly.Assembly from code.
//
// Usage:
//
//	go run ./hello                          # uses a temp dir, deleted on exit
//	go run ./hello --store=/path/to/store   # persistent location
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"scrinium.dev/composer"
	"scrinium.dev/engine/domain"
)

func main() {
	storeFlag := flag.String("store", "", "Store path (file:// or bare path). Empty = temporary directory deleted on exit.")
	flag.Parse()

	if err := run(*storeFlag); err != nil {
		log.Fatal(err)
	}
}

func run(storePath string) error {
	ctx := context.Background()

	// Resolve where the store lives. Empty flag → ephemeral temp dir;
	// explicit path → persistent. Real applications typically want the
	// explicit form.
	var (
		dir       string
		ephemeral bool
	)
	if storePath == "" {
		d, err := os.MkdirTemp("", "scrinium-hello-*")
		if err != nil {
			return fmt.Errorf("tempdir: %w", err)
		}
		dir = d
		ephemeral = true
		fmt.Printf("ephemeral store at %s (will be deleted)\n", dir)
	} else {
		dir = storePath
		fmt.Printf("persistent store at %s\n", dir)
	}
	defer func() {
		if !ephemeral {
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("cleanup tempdir: %v", err)
		}
	}()

	// Describe the store declaratively. This is the entire composer
	// document — a single local store, no encryption, default policy.
	// LoadOrInitYAML opens it if it exists, initialises it otherwise;
	// a typo'd URI or permission error surfaces directly rather than
	// silently creating a store somewhere unexpected. Production code
	// often picks one path explicitly (separate "init" and "run"
	// subcommands give operators an audit trail).
	config := fmt.Sprintf("store:\n  driver: file://%s\n", dir)
	asm, err := composer.LoadOrInitYAML(ctx, []byte(config))
	if err != nil {
		return fmt.Errorf("assemble: %w", err)
	}
	defer func() {
		if err := asm.Close(); err != nil {
			log.Printf("scrinium close: %v", err)
		}
	}()

	// Put one artifact. Payload is anything that satisfies io.Reader;
	// here a byte slice.
	body := []byte("hello, scrinium!\n")
	id, err := asm.Store().Put(ctx,
		domain.Artifact{Payload: bytes.NewReader(body)},
		domain.PutOptions{Namespace: "demo"},
	)
	if err != nil {
		return fmt.Errorf("put: %w", err)
	}
	fmt.Printf("stored: %s\n", id)

	// Read it back. Get returns a streaming ReadHandle — Close it when
	// done.
	rh, err := asm.Store().Get(ctx, id, domain.GetOptions{})
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	defer func() {
		if err := rh.Close(); err != nil {
			log.Printf("readhandle close: %v", err)
		}
	}()

	got, err := io.ReadAll(rh)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	fmt.Printf("read back: %s", got)

	return nil
}
