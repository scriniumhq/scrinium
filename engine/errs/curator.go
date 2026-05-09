package errs

import "errors"

// Curator: orchestration layer that composes Stores into a single
// namespace, runs HostStorage, drives Drain. See docs/2. Internals/01
// §1.5 for the role of Curator and §1.5.1 for HostStorage.

// ErrCuratorClosed — operation issued on a closed Curator.
var ErrCuratorClosed = errors.New("scrinium: curator closed")

// ErrStoreNotRegistered — Curator.Store(id) with an unknown id.
var ErrStoreNotRegistered = errors.New("scrinium: store not registered with curator")

// ErrHostStorageFull — HostStorage hit its hard limit while
// OnHostStorageFull: Reject was in effect; soft eviction was
// insufficient.
var ErrHostStorageFull = errors.New("scrinium: host storage full")

// ErrDrainNoTarget — at Drain time the Router returned an empty
// target list; follow-up behaviour is determined by
// OnDrainNoTarget.
var ErrDrainNoTarget = errors.New("scrinium: drain has no target")
