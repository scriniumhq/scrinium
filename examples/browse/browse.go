package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"

	"scrinium.dev"

	"scrinium.dev/domain"
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"
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
	asm, err := scrinium.LoadYAML(ctx, []byte(config))
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

	if err := asm.Store.Walk(ctx, func(m domain.Manifest) error {
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
	cap, capErr := asm.Store.Capacity(ctx)

	// --- Render ---

	fmt.Printf("Store: %s\n", storeURI)
	fmt.Printf("State: %s\n", asm.Store.State())
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

	// Loaded extensions, surfaced as whole units. Extensions() returns
	// nil when none are loaded.
	if exts := asm.Extensions(); len(exts) > 0 {
		fmt.Println("\nExtensions:")
		for _, d := range exts {
			fmt.Printf("  %s\n", d.Name)
		}
	}

	return nil
}
