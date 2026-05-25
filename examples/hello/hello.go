// hello is the smallest Scrinium program — store one artifact, read it
// back — shown three ways. Each variant is a single self-contained
// function: read it top to bottom and you see the whole example, from
// assembling the store to closing it, with nothing factored out.
//
// Usage:
//
//	go run ./hello open   [dir]          # one-liner: a driver URI, defaults for all else
//	go run ./hello config [dir]          # a Config built in code
//	go run ./hello manual [dir]          # by hand, every layer spelled out
//
// The three differ only in how the store comes to be. "open" is the
// shortest path a program can take; "config" is for programs that
// compute their configuration; "manual" spells out, layer by layer,
// what the first two do for you.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"scrinium.dev"
	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/store"
	"scrinium.dev/store/driver"
	"scrinium.dev/store/index"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hello open [dir] | hello config [dir] | hello manual [dir]")
		os.Exit(2)
	}
	dir := "/tmp/scrinium-hello"
	if len(os.Args) > 2 {
		dir = os.Args[2]
	}

	var err error
	switch os.Args[1] {
	case "open":
		err = runOpen(context.Background(), dir)
	case "config":
		err = runConfig(context.Background(), dir)
	case "manual":
		err = runManual(context.Background(), dir)
	default:
		fmt.Fprintln(os.Stderr, "usage: hello open [dir] | hello config [dir] | hello manual [dir]")
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// runOpen — the shortest path. Open takes a driver URI and applies a
// default index, default policy, and creates the store if it is absent.
// A *Scrinium IS a store, so Put/Get are called on it directly, and a
// single Close releases everything it assembled.
func runOpen(ctx context.Context, dir string) error {
	s, err := scrinium.Open(ctx, "file://"+dir)
	if err != nil {
		return err
	}
	defer s.Close()

	id, err := s.Put(ctx,
		domain.Artifact{Payload: bytes.NewReader([]byte("hello, scrinium!\n"))},
		domain.PutOptions{Namespace: "demo"})
	if err != nil {
		return fmt.Errorf("put: %w", err)
	}
	fmt.Printf("stored: %s\n", id)

	rh, err := s.Get(ctx, id, domain.GetOptions{})
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	fmt.Printf("read back: %s", got)
	return nil
}

// runConfig — assemble from a Config built in code, for programs that
// compute their configuration from flags, env, or a database rather
// than a file. Defaults are applied by Build, so a lone Store driver is
// already a complete, working store.
func runConfig(ctx context.Context, dir string) error {
	s, err := scrinium.Build(ctx, scrinium.Config{
		Store: &scrinium.StoreSpec{Driver: "file://" + dir},
	})
	if err != nil {
		return err
	}
	defer s.Close()

	id, err := s.Put(ctx,
		domain.Artifact{Payload: bytes.NewReader([]byte("hello, scrinium!\n"))},
		domain.PutOptions{Namespace: "demo"})
	if err != nil {
		return fmt.Errorf("put: %w", err)
	}
	fmt.Printf("stored: %s\n", id)

	rh, err := s.Get(ctx, id, domain.GetOptions{})
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	fmt.Printf("read back: %s", got)
	return nil
}

// runManual — assemble by hand, no assembler at all. This is exactly
// what the two paths above do for you, spelled out: dial a driver, dial
// an index, open-or-init the store wiring them together, and close both
// in order (the store does NOT close the index — its lifetime belongs
// to whoever dialed it, here this function). Use it when you need
// control the higher-level entry points do not expose, or just to see
// the layers.
func runManual(ctx context.Context, dir string) error {
	// Driver — the byte-level backend (here, local filesystem).
	drv, err := driver.DialDriver("file://" + dir)
	if err != nil {
		return fmt.Errorf("dial driver: %w", err)
	}

	// Index — the metadata catalogue (a sqlite file under the store).
	idx, err := index.DialIndex(ctx, "sqlite:///"+filepath.Join(dir, "index.db"))
	if err != nil {
		return fmt.Errorf("dial index: %w", err)
	}
	defer idx.Close()

	// Store — open if present, init otherwise.
	st, err := store.OpenStore(ctx, drv, store.WithStoreIndex(idx))
	if errors.Is(err, errs.ErrStoreNotFound) {
		st, _, err = store.InitStore(ctx, drv, store.WithStoreIndex(idx))
	}
	if err != nil {
		return fmt.Errorf("open/init store: %w", err)
	}
	defer st.Close()

	id, err := st.Put(ctx,
		domain.Artifact{Payload: bytes.NewReader([]byte("hello, scrinium!\n"))},
		domain.PutOptions{Namespace: "demo"})
	if err != nil {
		return fmt.Errorf("put: %w", err)
	}
	fmt.Printf("stored: %s\n", id)

	rh, err := st.Get(ctx, id, domain.GetOptions{})
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	fmt.Printf("read back: %s", got)
	return nil
}
