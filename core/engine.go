package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/rkurbatov/scrinium/compress"
	"github.com/rkurbatov/scrinium/drivers"
	"github.com/rkurbatov/scrinium/metrics"
)

type engine struct {
	driver drivers.NativeDriver
}

// NewInstance creates a new Scrinium core engine bound to a specific physical driver.
func NewInstance(d drivers.NativeDriver) Instance {
	return &engine{driver: d}
}

// Put streams data into a temporary location, computes metrics, and routes
// to the final CAS destination (with or without compression).
func (e *engine) Put(ctx context.Context, r io.Reader) (BlobManifest, error) {
	tmpPath := randomTmpPath()

	// 1. Stream to tmp while calculating metrics.
	// We use io.Pipe to stream into the driver without holding the file in memory.
	pr, pw := io.Pipe()
	pipeline := metrics.NewPipeline(metrics.WithSHA256(), metrics.WithEntropy())
	tee := io.TeeReader(r, pipeline)

	errChan := make(chan error, 1)
	go func() {
		defer pw.Close()
		_, err := io.Copy(pw, tee)
		errChan <- err
	}()

	if err := e.driver.Put(ctx, tmpPath, pr); err != nil {
		return BlobManifest{}, err
	}
	if err := <-errChan; err != nil {
		return BlobManifest{}, err
	}

	res := pipeline.Results()
	return e.finalizeIngest(ctx, tmpPath, res, false)
}

// IngestLocal is an optimized path for LocalFS drivers.
// It calculates metrics from the existing file and uses hardlinks (zero-copy) if possible.
func (e *engine) IngestLocal(ctx context.Context, physicalPath string, keepOriginal bool) (BlobManifest, func() error, error) {
	cleanup := func() error {
		if !keepOriginal {
			return os.Remove(physicalPath)
		}
		return nil
	}

	f, err := os.Open(physicalPath)
	if err != nil {
		return BlobManifest{}, nil, err
	}
	defer f.Close()

	pipeline := metrics.NewPipeline(metrics.WithSHA256(), metrics.WithEntropy())
	if _, err := io.Copy(io.Discard, io.TeeReader(f, pipeline)); err != nil {
		return BlobManifest{}, nil, err
	}

	res := pipeline.Results()
	manifest, err := e.finalizeIngest(ctx, physicalPath, res, true)
	return manifest, cleanup, err
}

func (e *engine) Open(ctx context.Context, payloadHash string) (drivers.File, error) {
	manifest, err := e.GetManifest(ctx, payloadHash)
	if err != nil {
		return nil, err
	}

	blobPath := buildBlobPath(payloadHash)
	physBlob, err := e.driver.Open(ctx, blobPath)
	if err != nil {
		return nil, err
	}

	if manifest.Archive.Status == ArchiveStatusArchived {
		return compress.NewChunkedReader(
			physBlob,
			manifest.Archive.ChunkSize,
			manifest.Archive.ChunkOffsets,
			manifest.SizeBytes,
		)
	}

	return physBlob, nil
}

func (e *engine) Materialize(ctx context.Context, payloadHash string, prefix, ext string) (string, func(), error) {
	manifest, err := e.GetManifest(ctx, payloadHash)
	if err != nil {
		return "", nil, err
	}

	// ZERO-COPY PATH: If uncompressed, return the actual file path
	// (Warning: this violates pure interface abstraction, but is required for legacy CLI tools).
	if manifest.Archive.Status == ArchiveStatusSkip {
		// We assume local driver uses this structure.
		// If using S3, we would still need to download it.
		return buildBlobPath(payloadHash), func() {}, nil
	}

	r, err := e.Open(ctx, payloadHash)
	if err != nil {
		return "", nil, err
	}
	defer r.Close()

	tmpFile, err := os.CreateTemp("", prefix+"-*"+ext)
	if err != nil {
		return "", nil, err
	}

	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}

	if _, err := io.Copy(tmpFile, r); err != nil {
		cleanup()
		return "", nil, err
	}
	_ = tmpFile.Sync()

	return tmpFile.Name(), cleanup, nil
}

func (e *engine) GetManifest(ctx context.Context, payloadHash string) (BlobManifest, error) {
	manPath := buildManifestPath(payloadHash)
	r, err := e.driver.Open(ctx, manPath)
	if err != nil {
		return BlobManifest{}, err
	}
	defer r.Close()

	var m BlobManifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return BlobManifest{}, err
	}
	return m, nil
}

// --- Internal Engine Logic ---

func (e *engine) finalizeIngest(ctx context.Context, srcPath string, res *metrics.Results, isLocalOSPath bool) (BlobManifest, error) {
	strategy := compress.GetStrategy(res.Size, res.Entropy, false)
	finalBlobPath := buildBlobPath(res.Hash)

	manifest := BlobManifest{
		Revision:    "v1",
		PayloadHash: res.Hash,
		SizeBytes:   res.Size,
		Entropy:     res.Entropy,
	}

	if !strategy.ShouldCompress {
		manifest.Archive.Status = ArchiveStatusSkip
		manifest.Archive.Size = res.Size

		if isLocalOSPath {
			if err := e.driver.LinkOrCopy(ctx, srcPath, finalBlobPath); err != nil {
				return BlobManifest{}, err
			}
		} else {
			if err := e.driver.Move(ctx, srcPath, finalBlobPath); err != nil {
				return BlobManifest{}, err
			}
		}
	} else {
		// Compress from srcPath to finalBlobPath via stream.
		srcFile, err := openSrc(ctx, e.driver, srcPath, isLocalOSPath)
		if err != nil {
			return BlobManifest{}, err
		}

		pr, pw := io.Pipe()
		physHasher := sha256.New()
		multiOut := io.MultiWriter(pw, physHasher)

		cw, err := compress.NewChunkedWriter(multiOut, strategy)
		if err != nil {
			srcFile.Close()
			return BlobManifest{}, err
		}

		errChan := make(chan error, 1)
		go func() {
			defer srcFile.Close()
			defer pw.Close()
			if _, copyErr := io.Copy(cw, srcFile); copyErr != nil {
				errChan <- copyErr
				return
			}
			if closeErr := cw.Close(); closeErr != nil {
				errChan <- closeErr
				return
			}
			errChan <- nil
		}()

		if err := e.driver.Put(ctx, finalBlobPath, pr); err != nil {
			return BlobManifest{}, err
		}
		if err := <-errChan; err != nil {
			return BlobManifest{}, err
		}

		manifest.Archive.Status = ArchiveStatusArchived
		manifest.Archive.Size = cw.EncodedSize()
		manifest.Archive.ChunkSize = strategy.ChunkSize
		manifest.Archive.ChunkOffsets = cw.Offsets()
		manifest.Archive.Hash = hex.EncodeToString(physHasher.Sum(nil))

		if !isLocalOSPath {
			_ = e.driver.Delete(ctx, srcPath)
		}
	}

	if err := e.saveManifest(ctx, &manifest); err != nil {
		return BlobManifest{}, err
	}

	return manifest, nil
}

func (e *engine) saveManifest(ctx context.Context, m *BlobManifest) error {
	// Two-pass serialization to seal the manifest
	m.ManifestHash = ""
	raw, _ := json.Marshal(m)
	h := sha256.Sum256(raw)
	m.ManifestHash = hex.EncodeToString(h[:])

	finalJSON, _ := json.MarshalIndent(m, "", "  ")
	path := buildManifestPath(m.PayloadHash)
	return e.driver.Put(ctx, path, bytes.NewReader(finalJSON))
}

func openSrc(ctx context.Context, driver drivers.NativeDriver, p string, isLocalOSPath bool) (io.ReadCloser, error) {
	if isLocalOSPath {
		return os.Open(p)
	}
	return driver.Open(ctx, p)
}

func randomTmpPath() string {
	b := make([]byte, 16)
	rand.Read(b)
	return path.Join("tmp", fmt.Sprintf("put-%x.tmp", b))
}

func buildBlobPath(hash string) string {
	if len(hash) < 4 {
		return path.Join("blobs", hash)
	}
	return path.Join("blobs", hash[:2], hash[2:])
}

func buildManifestPath(hash string) string {
	if len(hash) < 4 {
		return path.Join("manifests", hash+".meta.json")
	}
	return path.Join("manifests", hash[:2], hash[2:]+".meta.json")
}
