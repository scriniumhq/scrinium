package errs

import "errors"

// ErrStopWalk is the early-exit sentinel for any iteration callback
// in the engine: Store.Walk, Store.WalkSystem, StoreIndex.List*,
// Driver.ListObjectsWithModTime. Returning this from the callback
// stops the walk without an error — the function returns nil to
// its caller.
//
// One sentinel covers every walk because the meaning is identical
// across layers. Splitting it ("driver-level walk" vs "store-level
// walk") was tried and only forced callers to choose between two
// values that mean the same thing.
var ErrStopWalk = errors.New("scrinium: stop walk")
