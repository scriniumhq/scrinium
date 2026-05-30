// hello-manual assembles a Scrinium store by hand, with no scrinium
// front door — exactly what scrinium.Open / scrinium.Build do for you,
// spelled out one layer at a time. Read it to see what the high-level
// hello (../hello) hides: dial a driver, dial an index, open-or-init
// the store wiring them together, and close both in the right order.
//
// Usage:
//
//	go run ./hello-manual                # store under /tmp
//	go run ./hello-manual -dir /data/app # custom directory
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
)

func main() {
	dir := flag.String("dir", "/tmp/scrinium-hello", "store directory")
	flag.Parse()

	ctx := context.Background()

	// Driver — the byte-level backend (here, the local filesystem).
	drv, err := driver.DialDriver("file://" + *dir)
	if err != nil {
		log.Fatalf("dial driver: %v", err)
	}

	// Index — the metadata catalogue. scrinium defaults this to a
	// sqlite file under a local store; we dial the same by hand. The
	// index lifetime belongs to whoever dialed it (this function), so
	// we are responsible for closing it — the store will not.
	idx, err := index.DialIndex(ctx, "sqlite:///"+filepath.Join(*dir, "index.db"))
	if err != nil {
		log.Fatalf("dial index: %v", err)
	}
	defer idx.Close()

	// Store — open if present, init otherwise. scrinium's open-or-init
	// branches on ErrStoreNotFound; here it is explicit.
	st, err := store.OpenStore(ctx, drv, store.WithStoreIndex(idx))
	if errors.Is(err, errs.ErrStoreNotFound) {
		st, _, err = store.InitStore(ctx, drv, store.WithStoreIndex(idx))
	}
	if err != nil {
		log.Fatalf("open/init store: %v", err)
	}
	defer st.Close()

	// Store an artifact.
	id, err := st.Put(ctx,
		domain.Artifact{Payload: strings.NewReader("hello, scrinium!\n")},
		store.WithNamespace("demo"))
	if err != nil {
		log.Fatalf("put: %v", err)
	}
	fmt.Printf("stored: %s\n", id)

	// Read it back.
	rh, err := st.Get(ctx, id)
	if err != nil {
		log.Fatalf("get: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		log.Fatalf("read: %v", err)
	}
	fmt.Printf("read back: %s", got)
}
