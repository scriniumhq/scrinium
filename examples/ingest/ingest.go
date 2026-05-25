// ingest scans a directory tree and stores every regular file as an
// artifact, with its relative path attached as fsmeta so the
// projection View renders it under by-path/.
//
// Demonstrates:
//
//   - composer.LoadOrInitYAML to assemble (creating on first run) a
//     store from an inline config.
//   - filepath.WalkDir for batch traversal.
//   - Attaching fsmeta metadata so artifacts have a virtual path.
//   - SessionID + RollbackSession idiom for atomic-ish batches: a
//     failure mid-ingest leaves a known set of artifacts to roll back,
//     not orphans scattered across timestamps.
//
// Usage:
//
//	go run ./ingest --src=/path/to/files --store=/tmp/my-store [--namespace=foo]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"scrinium.dev/composer"
	"scrinium.dev/domain"
	"scrinium.dev/engine/projection/fsmeta"
)

func main() {
	src := flag.String("src", "", "Source directory to ingest (required)")
	store := flag.String("store", "", "Store path (required, file:// or bare path)")
	namespace := flag.String("namespace", "default", "Namespace for ingested artifacts")
	flag.Parse()

	if *src == "" || *store == "" {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*src, *store, *namespace); err != nil {
		log.Fatal(err)
	}
}

func run(srcDir, storeURI, namespace string) error {
	ctx := context.Background()

	// Assemble, creating on first run. LoadOrInitYAML only initialises
	// when the store does not yet exist; a typo'd URI or a permission
	// error surfaces directly, avoiding the "silently created an empty
	// store somewhere unexpected" trap. Production code typically
	// chooses one path explicitly (separate "init" and "ingest"
	// subcommands).
	config := fmt.Sprintf("store:\n  driver: %s\n", storeURI)
	asm, err := composer.LoadOrInitYAML(ctx, []byte(config))
	if err != nil {
		return fmt.Errorf("assemble: %w", err)
	}
	defer func() {
		if err := asm.Close(); err != nil {
			log.Printf("close: %v", err)
		}
	}()

	// One SessionID per ingest run. RollbackSession on this id wipes
	// everything we wrote in this run — useful for failed batches.
	sessionID := domain.SessionID("ingest-" + uuid.New().String())
	fmt.Printf("session: %s\n", sessionID)

	var (
		count int
		bytes int64
	)
	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			// Skip symlinks, sockets, devices.
			return nil
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		// Normalise to forward slashes (fsmeta's contract) and guard
		// against accidental ".." parents.
		virtualPath := filepath.ToSlash(rel)
		if strings.HasPrefix(virtualPath, "../") || strings.Contains(virtualPath, "/../") {
			return fmt.Errorf("escapes source root: %s", rel)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() {
			if err := f.Close(); err != nil {
				log.Printf("close %s: %v", virtualPath, err)
			}
		}()

		// Attach virtual-path metadata so the View can render the
		// artifact under by-path/.
		md, err := fsmeta.Encode(fsmeta.FileSystem{
			Kind:    fsmeta.Marker,
			Path:    virtualPath,
			Mode:    uint32(info.Mode().Perm()),
			ModTime: info.ModTime(),
		})
		if err != nil {
			return fmt.Errorf("encode fsmeta: %w", err)
		}

		id, err := asm.Store().Put(ctx,
			domain.Artifact{Payload: f, Ext: md},
			domain.PutOptions{
				SessionID: sessionID,
				Namespace: namespace,
			},
		)
		if err != nil {
			return fmt.Errorf("put %s: %w", virtualPath, err)
		}

		count++
		bytes += info.Size()
		fmt.Printf("  %s  →  %s\n", virtualPath, id)
		return nil
	})

	if walkErr != nil {
		// Best-effort rollback. Failures here are logged; a retry of
		// the ingest with the same SessionID-prefix scheme would resume
		// cleanly because RollbackSession is idempotent.
		fmt.Fprintf(os.Stderr, "walk failed, rolling back: %v\n", walkErr)
		if rbErr := asm.Store().RollbackSession(ctx, sessionID); rbErr != nil && !errors.Is(rbErr, context.Canceled) {
			fmt.Fprintf(os.Stderr, "rollback: %v\n", rbErr)
		}
		return walkErr
	}

	fmt.Printf("\ningested %d files (%d bytes)\n", count, bytes)
	return nil
}
