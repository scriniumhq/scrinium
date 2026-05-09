// browse opens an existing Scrinium store read-only and prints
// a summary: total artifact count, total bytes referenced,
// per-namespace breakdown, and capacity from the underlying
// driver.
//
// Demonstrates:
//
//   - Opening a store with ReadOnly=true (no writes possible,
//     no scratch directory needed).
//   - Iterating manifests via Store.Walk.
//   - Pulling capacity figures via Store.Capacity.
//
// Usage:
//
//	go run ./browse --store=/tmp/my-store
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/rkurbatov/scrinium"
	"github.com/rkurbatov/scrinium/engine/domain"
)

func main() {
	store := flag.String("store", "", "Store path (required, file:// or bare path)")
	flag.Parse()

	if *store == "" {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*store); err != nil {
		log.Fatal(err)
	}
}

func run(storeURI string) error {
	ctx := context.Background()

	cfg := scrinium.DefaultConfig()
	cfg.Store = storeURI
	cfg.ReadOnly = true

	s, err := scrinium.Open(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			log.Printf("close: %v", err)
		}
	}()

	// Aggregate stats across all namespaces.
	type nsStats struct {
		count int
		bytes int64
	}
	byNS := make(map[string]*nsStats)

	if err := s.Store.Walk(ctx, "*", func(m domain.Manifest) error {
		st, ok := byNS[m.Namespace]
		if !ok {
			st = &nsStats{}
			byNS[m.Namespace] = st
		}
		st.count++
		st.bytes += m.OriginalSize
		return nil
	}); err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	// Capacity is best-effort: a slow driver shouldn't hang
	// `browse`. Plain Open already capped its own probes; here
	// we accept whatever Capacity returns.
	cap, capErr := s.Store.Capacity(ctx)

	// --- Render ---

	fmt.Printf("Store: %s\n", storeURI)
	fmt.Printf("State: %s\n", s.Store.State())
	fmt.Println()

	if len(byNS) == 0 {
		fmt.Println("(no artifacts)")
	} else {
		// Stable order for deterministic output.
		names := make([]string, 0, len(byNS))
		for k := range byNS {
			names = append(names, k)
		}
		sort.Strings(names)

		var totalCount int
		var totalBytes int64
		fmt.Printf("%-32s %10s %15s\n", "namespace", "artifacts", "bytes")
		fmt.Println("------------------------------------------------------------")
		for _, ns := range names {
			st := byNS[ns]
			label := ns
			if label == "" {
				label = "(default)"
			}
			fmt.Printf("%-32s %10d %15d\n", label, st.count, st.bytes)
			totalCount += st.count
			totalBytes += st.bytes
		}
		fmt.Println("------------------------------------------------------------")
		fmt.Printf("%-32s %10d %15d\n", "TOTAL", totalCount, totalBytes)
	}

	fmt.Println()
	if capErr == nil {
		fmt.Printf("Capacity: %d bytes used / %d bytes available / %d bytes total\n",
			cap.UsedBytes, cap.AvailableBytes, cap.TotalBytes)
		fmt.Printf("Counts:   %d artifacts, %d blobs\n",
			cap.ArtifactCount, cap.BlobCount)
	} else {
		fmt.Printf("Capacity: unavailable (%v)\n", capErr)
	}

	exts := s.ListExtensions()
	if len(exts) > 0 {
		fmt.Println("\nIndex extensions:")
		for _, e := range exts {
			fmt.Printf("  %s (schema v%d)\n", e.Name, e.SchemaVersion)
		}
	}

	return nil
}
