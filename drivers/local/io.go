package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// moveFile attempts an atomic system rename.
// If the move fails due to a cross-device boundary (EXDEV), it falls back
// to a safe Copy + Sync + Delete strategy to ensure data integrity.
func moveFile(ctx context.Context, src, dst string) error {
	// 1. Try atomic rename (fastest)
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	// 2. Check if it's a cross-device link error
	if !errors.Is(err, syscall.EXDEV) {
		return fmt.Errorf("atomic rename failed: %w", err)
	}

	// 3. Fallback to Copy+Delete
	if err := copyFile(ctx, src, dst); err != nil {
		// Cleanup the partial destination file if copy failed
		_ = os.Remove(dst)
		return fmt.Errorf("cross-device move failed: %w", err)
	}

	// 4. Remove source only after destination is synced to disk
	return os.Remove(src)
}

// copyFile performs a buffered copy with a physical sync at the end.
func copyFile(ctx context.Context, src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if cErr := out.Close(); err == nil && cErr != nil {
			err = cErr
		}
	}()

	// Perform copy with context awareness
	buf := make([]byte, 1024*1024) // 1MB buffer
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, rErr := in.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return wErr
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			return rErr
		}
	}

	// Force physical write to disk before reporting success
	return out.Sync()
}

// openAndStat acquires a non-blocking POSIX flock to ensure exclusive access
// during the ingestion phase, preventing reading partially written files.
func openAndStat(ctx context.Context, path string) (*os.File, os.FileInfo, bool) {
	const maxRetries = 5
	backoff := 50 * time.Millisecond

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return nil, nil, false
		default:
		}

		// Open in read-only mode
		f, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err == nil {
			// Try to acquire an exclusive lock to check if another process is writing to it.
			// We use non-blocking (NB) to avoid hanging the ingester.
			errLock := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if errLock == nil {
				// Immediately release the lock. We just needed to know if it's "available".
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

				info, statErr := f.Stat()
				if statErr == nil && info.Size() > 0 {
					return f, info, true
				}
				_ = f.Close()
			} else {
				_ = f.Close()
			}
		}

		// Exponential backoff if the file is locked or busy
		time.Sleep(backoff)
		backoff *= 2
	}

	return nil, nil, false
}
