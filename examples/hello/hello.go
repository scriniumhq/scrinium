// hello is the smallest Scrinium program: store one artifact and read
// it back, using the high-level scrinium front door.
//
// Usage:
//
//	go run ./hello                       # config built in code, store under /tmp
//	go run ./hello -dir /data/app        # same, custom directory
//	go run ./hello -c store.yaml         # configuration read from a YAML file
//
// With -c the configuration comes from a file (how a deployed service
// is usually wired: operators edit the file). Without it, the same
// configuration is built in code — the path for programs that compute
// their setup from flags, env, or a database. Either way a *Scrinium
// is a store: Put/Get are called on it directly, and one Close releases
// everything it assembled.
//
// To see what scrinium does under the hood, read ../hello-manual, which
// assembles the same store from primitives with no front door at all.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"scrinium.dev"
	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
)

func main() {
	configPath := flag.String("c", "", "path to a YAML configuration file; if empty, config is built in code")
	dir := flag.String("dir", "/tmp/scrinium-hello", "store directory (ignored when -c is given)")
	flag.Parse()

	ctx := context.Background()

	// Assemble the store: from a YAML file when -c is given, otherwise
	// from a Config built in code. Both yield the same *Scrinium.
	var (
		s   *scrinium.Scrinium
		err error
	)
	if *configPath != "" {
		data, rerr := os.ReadFile(*configPath)
		if rerr != nil {
			log.Fatal(rerr)
		}
		s, err = scrinium.LoadOrInitYAML(ctx, data)
	} else {
		s, err = scrinium.Build(ctx, scrinium.Config{
			Store: &scrinium.StoreSpec{Driver: "file://" + *dir},
		})
	}
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	// Store an artifact.
	id, err := s.Put(ctx,
		domain.Artifact{Payload: strings.NewReader("hello, scrinium!\n")},
		store.WithNamespace("demo"))
	if err != nil {
		log.Fatalf("put: %v", err)
	}
	fmt.Printf("stored: %s\n", id)

	// Read it back.
	rh, err := s.Get(ctx, id)
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
