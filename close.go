package scrinium

import (
	"errors"
	"fmt"
)

// Close releases the runtime's resources in reverse order of
// Open. Errors are accumulated; every resource is given a
// chance to close before we return. Multiple errors are joined
// via errors.Join so callers can errors.Is/As any of them.
//
// Close is idempotent: the first call performs the shutdown,
// subsequent calls return the same error without doing
// anything. This matches the io.Closer convention used across
// the stdlib (*os.File, *sql.DB) — a `defer s.Close()` paired
// with an explicit s.Close() in the main path is safe.
//
// Close is also tolerant of partially-constructed Scrinium
// values: any nil resource is skipped. This allows tests and
// supervisors that abort partway through Open to call Close
// without a nil-pointer panic.
//
// Order matters for the resources that ARE present:
//
//   - View first: stops backfill goroutines so they aren't
//     reading from a Store that's about to be closed.
//   - FSIndex next: clears its reference to the StoreIndex
//     so any stray post-close GetByID calls fail cleanly
//     ("not registered") instead of touching a closed handle.
//   - Store next: wipes secrets (DEK, capability token,
//     default StaticKeyResolver). We want secrets gone before
//     the index is torn down (sqlite teardown can flush dirty
//     pages — minor, but the principle is "secrets first").
//   - Index last: releases the sqlite handle (or future PG
//     connection pool). Leaked DB handles keep the file
//     locked on Windows and waste fds elsewhere.
//
// The Driver (localfs) holds nothing closable.
func (s *Scrinium) Close() error {
	s.closeOnce.Do(func() {
		var errs []error

		if s.View != nil {
			if err := s.View.Close(); err != nil {
				errs = append(errs, fmt.Errorf("view close: %w", err))
			}
		}

		if s.FSIndex != nil {
			if err := s.FSIndex.Close(); err != nil {
				errs = append(errs, fmt.Errorf("fsindex close: %w", err))
			}
		}

		if s.Store != nil {
			if err := s.Store.Close(); err != nil {
				errs = append(errs, fmt.Errorf("store close: %w", err))
			}
		}

		if s.Index != nil {
			if err := s.Index.Close(); err != nil {
				errs = append(errs, fmt.Errorf("index close: %w", err))
			}
		}

		if len(errs) > 0 {
			s.closeErr = errors.Join(errs...)
		}
	})
	return s.closeErr
}
