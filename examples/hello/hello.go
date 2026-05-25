// hello is the smallest Scrinium program — store one artifact, read it
// back — shown three ways. The point is the contrast: putAndGet at the
// bottom is identical in all three; only how the store is assembled
// differs.
//
// Usage:
//
//	go run ./hello yaml   store.yaml     # assemble from a YAML config file
//	go run ./hello config [dir]          # assemble from a Config built in code
//	go run ./hello manual [dir]          # assemble by hand, no composer
//
// Pick by reading top to bottom: "yaml" is how a deployed service is
// usually wired (operators edit a file); "config" is for programs that
// compute their configuration; "manual" spells out what composer does
// for you, one layer at a time.
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

	"scrinium.dev/composer"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()

	var (
		st      store.Store
		cleanup func()
		err     error
	)
	switch os.Args[1] {
	case "yaml":
		if len(os.Args) != 3 {
			usage()
		}
		st, cleanup, err = fromYAML(ctx, os.Args[2])
	case "config":
		st, cleanup, err = fromConfig(ctx, target(2, "/tmp/scrinium-hello"))
	case "manual":
		st, cleanup, err = fromManual(ctx, target(2, "/tmp/scrinium-hello"))
	default:
		usage()
	}
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	if err := putAndGet(ctx, st); err != nil {
		log.Fatal(err)
	}
}

// fromYAML — assemble from a YAML config document read off disk. This is
// how a real service is usually wired: operators edit the file, the
// program just reads it and knows nothing about drivers or policies.
func fromYAML(ctx context.Context, path string) (store.Store, func(), error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	asm, err := composer.LoadOrInitYAML(ctx, data)
	if err != nil {
		return nil, nil, err
	}
	return asm.Store(), closeFn(asm), nil
}

// fromConfig — assemble from a composer.Config built in code, no YAML
// text. The path for programs that compute their configuration from
// flags, env, or a database. Defaults are applied by Build, so a lone
// Store driver is already a complete, working store.
func fromConfig(ctx context.Context, dir string) (store.Store, func(), error) {
	asm, err := composer.Build(ctx, composer.Config{
		Store: &composer.StoreSpec{Driver: "file://" + dir},
	})
	if err != nil {
		return nil, nil, err
	}
	return asm.Store(), closeFn(asm), nil
}

// fromManual — assemble by hand, no composer at all. This is exactly
// what the two paths above do for you, spelled out: dial a driver, dial
// an index, open-or-init the store wiring them together. Use it when
// you need control composer does not expose, or to see the layers.
func fromManual(ctx context.Context, dir string) (store.Store, func(), error) {
	// Driver — the byte-level backend (here, local filesystem).
	drv, err := driver.DialDriver("file://" + dir)
	if err != nil {
		return nil, nil, fmt.Errorf("dial driver: %w", err)
	}

	// Index — the metadata catalogue. composer defaults this to a
	// sqlite file under a local store; we do the same by hand.
	idx, err := index.DialIndex(ctx, "sqlite:///"+filepath.Join(dir, "index.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("dial index: %w", err)
	}

	// Store — open if present, init otherwise (composer's OpenOrInit
	// branches on ErrStoreNotFound; here it is explicit).
	st, err := store.OpenStore(ctx, drv, store.WithStoreIndex(idx))
	if errors.Is(err, errs.ErrStoreNotFound) {
		st, _, err = store.InitStore(ctx, drv, store.WithStoreIndex(idx))
	}
	if err != nil {
		idx.Close()
		return nil, nil, fmt.Errorf("open/init store: %w", err)
	}
	return st, func() {
		if err := st.Close(); err != nil {
			log.Printf("store close: %v", err)
		}
		if err := idx.Close(); err != nil {
			log.Printf("index close: %v", err)
		}
	}, nil
}

// putAndGet stores one artifact and reads it back. Identical for all
// three assembly methods — that is the whole point.
func putAndGet(ctx context.Context, st store.Store) error {
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

// closeFn adapts an assembly's Close to the cleanup signature, logging
// any error (the composer paths own a whole assembly, not just a store).
func closeFn(asm interface{ Close() error }) func() {
	return func() {
		if err := asm.Close(); err != nil {
			log.Printf("assembly close: %v", err)
		}
	}
}

func target(i int, def string) string {
	if len(os.Args) > i {
		return os.Args[i]
	}
	return def
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hello yaml <config.yaml> | hello config [dir] | hello manual [dir]")
	os.Exit(2)
}
