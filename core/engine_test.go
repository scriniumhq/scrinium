package core_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/drivers/local"
)

func TestEngine_PutAndOpen(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()

	driver := local.NewDriver("local-1", tmpRoot)
	engine := core.NewInstance(driver)

	// 1. Generate highly compressible payload (low entropy)
	payloadStr := strings.Repeat("hello scrinium payload ", 10000)
	payloadSize := int64(len(payloadStr))

	// 2. Put Payload
	manifest, err := engine.Put(ctx, bytes.NewReader([]byte(payloadStr)))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if manifest.SizeBytes != payloadSize {
		t.Fatalf("expected size %d, got %d", payloadSize, manifest.SizeBytes)
	}
	if manifest.Archive.Status != core.ArchiveStatusArchived {
		t.Fatalf("expected payload to be compressed due to low entropy, got status: %s", manifest.Archive.Status)
	}

	// 3. Get Manifest directly
	retrievedManifest, err := engine.GetManifest(ctx, manifest.PayloadHash)
	if err != nil {
		t.Fatalf("GetManifest failed: %v", err)
	}
	if retrievedManifest.ManifestHash != manifest.ManifestHash {
		t.Fatal("manifest hashes do not match")
	}

	// 4. Open Payload (Sequential)
	r, err := engine.Open(ctx, manifest.PayloadHash)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer r.Close()

	size, _ := r.Size()
	if size != payloadSize {
		t.Fatalf("Open().Size() expected %d, got %d", payloadSize, size)
	}

	outBytes, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(outBytes) != payloadStr {
		t.Fatal("decompressed content does not match original")
	}

	// 5. Open Payload (ReadAt - Concurrent Pool check)
	readAtBuf := make([]byte, 20)
	n, err := r.ReadAt(readAtBuf, 6) // "scrinium payload ..."
	if err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if n != 20 {
		t.Fatalf("ReadAt expected 20 bytes, got %d", n)
	}
	if string(readAtBuf) != "scrinium payload hel" {
		t.Fatalf("ReadAt content mismatch, got: %s", string(readAtBuf))
	}
}

func TestEngine_IngestLocal(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	driver := local.NewDriver("local-1", tmpRoot)
	engine := core.NewInstance(driver)

	// Create random external file (high entropy -> skip compression)
	extFile := filepath.Join(tmpRoot, "external.bin")
	highEntropyData := make([]byte, 1024*10)
	for i := range highEntropyData {
		highEntropyData[i] = byte(i % 256)
	}
	_ = os.WriteFile(extFile, highEntropyData, 0644)

	manifest, cleanup, err := engine.IngestLocal(ctx, extFile, false)
	if err != nil {
		t.Fatalf("IngestLocal failed: %v", err)
	}
	defer cleanup()

	if manifest.Archive.Status != core.ArchiveStatusSkip {
		t.Fatalf("expected high entropy file to skip compression")
	}

	r, err := engine.Open(ctx, manifest.PayloadHash)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer r.Close()

	out, _ := io.ReadAll(r)
	if !bytes.Equal(out, highEntropyData) {
		t.Fatal("content mismatch after IngestLocal")
	}
}
