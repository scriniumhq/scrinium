// hello is the smallest Scrinium program: it creates (or opens)
// a store, puts one artifact, reads it back, and closes. About
// 120 lines, all of which serve a purpose — feel free to copy
// it as a starting point for your own integration.
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

	"scrinium.dev"
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

	// Resolve where the store lives. Empty flag → ephemeral
	// temp dir; explicit path → persistent. Real applications
	// typically want the explicit form.
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

	// Open existing store or create new. We probe by trying
	// Open; if the store has not been initialised yet (no
	// descriptor on disk), Open fails and we fall through to
	// Init. Production code typically chooses one path
	// explicitly — separating "init" and "run" subcommands.
	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + dir
	s, err := scrinium.Open(ctx, cfg)
	if err != nil {
		s, _, err = scrinium.Init(ctx, cfg)
		if err != nil {
			return fmt.Errorf("init: %w", err)
		}
	}
	defer func() {
		if err := s.Close(); err != nil {
			log.Printf("scrinium close: %v", err)
		}
	}()

	// Put one artifact. Payload is anything that satisfies
	// io.Reader; here a byte slice.
	body := []byte("hello, scrinium!\n")
	id, err := s.Store.Put(ctx,
		domain.Artifact{Payload: bytes.NewReader(body)},
		domain.PutOptions{Namespace: "demo"},
	)
	if err != nil {
		return fmt.Errorf("put: %w", err)
	}
	fmt.Printf("stored: %s\n", id)

	// Read it back. Get returns a streaming ReadHandle — Close
	// it when done.
	rh, err := s.Store.Get(ctx, id, domain.GetOptions{})
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
