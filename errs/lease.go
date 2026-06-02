package errs

import "errors"

// Lease coordination across hosts.

// ErrLeaseHeld — an attempt to acquire a lease held by an active
// owner.
var ErrLeaseHeld = errors.New("scrinium: lease held")

// ErrLeaseLost — the lease was lost in flight or right after a
// takeover (concurrent steal).
var ErrLeaseLost = errors.New("scrinium: lease lost")
