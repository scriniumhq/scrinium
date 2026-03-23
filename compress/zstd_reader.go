package compress

import (
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/rkurbatov/scrinium/drivers"
)

// decoderPool provides stateless zstd decoders for highly concurrent ReadAt operations.
// This prevents allocating ~2-4MB per concurrent read and eliminates the need for mutexes.
var decoderPool = sync.Pool{
	New: func() any {
		dec, _ := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		return dec
	},
}

// ChunkedReader implements io.ReadSeekCloser for payloads stored as Zstd frames.
// It supports highly concurrent, lock-free random access (ReadAt) alongside standard sequential reads.
type ChunkedReader struct {
	physBlob     drivers.File
	chunkSize    int
	chunkOffsets []int64

	// State for sequential operations (Read/Seek)
	decoder      *zstd.Decoder
	virtPos      int64
	virtSize     int64
	currentChunk int
}

// NewChunkedReader initializes a decompressing reader for a Payload.
func NewChunkedReader(physBlob drivers.File, chunkSize int, offsets []int64, virtSize int64) (*ChunkedReader, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		return nil, fmt.Errorf("failed to init zstd sequential reader: %w", err)
	}

	return &ChunkedReader{
		physBlob:     physBlob,
		chunkSize:    chunkSize,
		chunkOffsets: offsets,
		decoder:      dec,
		virtSize:     virtSize,
		currentChunk: -1,
	}, nil
}

// Read implements io.Reader for sequential reads. Inherently stateful.
func (r *ChunkedReader) Read(p []byte) (n int, err error) {
	if r.virtPos >= r.virtSize {
		return 0, io.EOF
	}

	if err := r.syncChunk(); err != nil {
		return 0, err
	}

	n, err = r.decoder.Read(p)
	r.virtPos += int64(n)

	// If frame ends but content continues, reset chunk index to load next frame.
	if err == io.EOF && r.virtPos < r.virtSize {
		r.currentChunk = -1
		return n, nil
	}

	return n, err
}

// Seek implements io.Seeker for sequential reads.
func (r *ChunkedReader) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = r.virtPos + offset
	case io.SeekEnd:
		newPos = r.virtSize + offset
	default:
		return 0, fmt.Errorf("invalid seek whence: %d", whence)
	}

	if newPos < 0 || newPos > r.virtSize {
		return 0, fmt.Errorf("seek out of bounds: %d", newPos)
	}

	if newPos == r.virtPos {
		return r.virtPos, nil
	}

	// Invalidate chunk if we jumped outside current frame boundaries.
	newChunk := int(newPos / int64(r.chunkSize))
	if newChunk != r.currentChunk {
		r.currentChunk = -1
	}

	r.virtPos = newPos
	return r.virtPos, nil
}

// syncChunk aligns the physical and logical pointers for sequential Read().
func (r *ChunkedReader) syncChunk() error {
	targetChunk := int(r.virtPos / int64(r.chunkSize))
	if targetChunk == r.currentChunk {
		return nil
	}

	physOffset := r.chunkOffsets[targetChunk]
	if _, err := r.physBlob.Seek(physOffset, io.SeekStart); err != nil {
		return err
	}

	if err := r.decoder.Reset(r.physBlob); err != nil {
		return err
	}

	r.currentChunk = targetChunk

	discard := r.virtPos % int64(r.chunkSize)
	if discard > 0 {
		if _, err := io.CopyN(io.Discard, r.decoder, discard); err != nil {
			return err
		}
	}

	return nil
}

// ReadAt implements io.ReaderAt.
// It is completely stateless, lock-free, and safe for high concurrency.
// It seamlessly handles reads that cross physical chunk boundaries.
func (r *ChunkedReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= r.virtSize {
		return 0, io.EOF
	}

	// Borrow a stateless decoder from the pool
	dec := decoderPool.Get().(*zstd.Decoder)
	defer decoderPool.Put(dec)

	physSize, err := r.physBlob.Size()
	if err != nil {
		return 0, err
	}

	var bytesRead int
	pLen := len(p)

	// Loop handles the case where the requested bytes span across multiple chunks
	for bytesRead < pLen {
		currOff := off + int64(bytesRead)
		if currOff >= r.virtSize {
			return bytesRead, io.EOF
		}

		chunkIdx := int(currOff / int64(r.chunkSize))
		physOff := r.chunkOffsets[chunkIdx]

		// io.SectionReader uses r.physBlob.ReadAt under the hood.
		// It never moves the physical *os.File seek pointer.
		section := io.NewSectionReader(r.physBlob, physOff, physSize-physOff)

		if err := dec.Reset(section); err != nil {
			return bytesRead, err
		}

		// Calculate offset inside the uncompressed chunk and discard leading bytes
		chunkInternalOff := currOff % int64(r.chunkSize)
		if chunkInternalOff > 0 {
			if _, err := io.CopyN(io.Discard, dec, chunkInternalOff); err != nil {
				return bytesRead, err
			}
		}

		// Calculate how many bytes we can read from THIS specific chunk
		remInP := pLen - bytesRead
		remInChunk := int(int64(r.chunkSize) - chunkInternalOff)

		toRead := remInP
		if toRead > remInChunk {
			toRead = remInChunk
		}

		// Read exactly what belongs to this chunk
		limitReader := io.LimitReader(dec, int64(toRead))
		readThisChunk, err := io.ReadFull(limitReader, p[bytesRead:bytesRead+toRead])
		bytesRead += readThisChunk

		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return bytesRead, err
		}
	}

	return bytesRead, nil
}

// Size returns the logical (uncompressed) size of the Payload.
func (r *ChunkedReader) Size() (int64, error) {
	return r.virtSize, nil
}

// Close gracefully releases the sequential decoder resources.
func (r *ChunkedReader) Close() error {
	r.decoder.Close()
	return r.physBlob.Close()
}
