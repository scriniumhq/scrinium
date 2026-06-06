package agent

import (
	"context"
	"errors"
)

// IsCtxErr reports whether err is a context cancellation/deadline.
func IsCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// FirstNonCtxErr returns the first non-nil, non-context error.
func FirstNonCtxErr(errs ...error) error {
	for _, e := range errs {
		if e != nil && !IsCtxErr(e) {
			return e
		}
	}
	return nil
}
