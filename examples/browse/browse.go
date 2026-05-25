// browse opens an existing Scrinium store read-only and prints a
// summary: total artifact count, total bytes referenced, per-namespace
// breakdown, and capacity from the underlying driver.
//
// Demonstrates:
//
//   - Assembling a store read-only from a composer config
//     (projection.readOnly: true — no writes, no scratch dir needed).
//   - Iterating manifests via Store.Walk.
//   - Pulling capacity figures via Store.Capacity.
//   - Listing index extensions via the ExtensionLister capability.
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

	"scrinium.dev/domain"
	"scrinium.dev/internal/assembly"
	"scrinium.dev/store/index"
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

	// Assemble read-only. LoadYAML opens an existing store (never
	// initialises); the projection section's readOnly flag means no
	// writes are possible and no scratch directory is created.
	config := fmt.Sprintf("store:\n  driver: %s\nprojection:\n  readOnly: true\n", storeURI)
	asm, err := assembly.LoadYAML(ctx, []byte(config))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() {
		if err := asm.Close(); err != nil {
			log.Printf("close: %v", err)
		}
	}()

	// Aggregate stats across all namespaces.
	type nsStats struct {
		count int
		bytes int64
	}
	byNS := make(map[string]*nsStats)

	if err := asm.Store().Walk(ctx, "*", func(m domain.Manifest) error {
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

	// Capacity is best-effort: a slow driver shouldn't hang `browse`.
	cap, capErr := asm.Store().Capacity(ctx)

	// --- Render ---

	fmt.Printf("Store: %s\n", storeURI)
	fmt.Printf("State: %s\n", asm.Store().State())
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

	// Index extensions are an optional capability — present only if the
	// index backend implements ExtensionLister.
	if lister, ok := asm.Index().(index.ExtensionLister); ok {
		exts := lister.ListExtensions()
		if len(exts) > 0 {
			fmt.Println("\nIndex extensions:")
			for _, e := range exts {
				fmt.Printf("  %s (schema v%d)\n", e.Name, e.SchemaVersion)
			}
		}
	}

	return nil
}
