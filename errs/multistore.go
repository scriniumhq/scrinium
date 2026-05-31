package errs

import "errors"

// ErrStoreNotRegistered — multistore.Store(id) called with an
// unknown StoreID.
var ErrStoreNotRegistered = errors.New("scrinium: store not registered")

// ErrMultistoreClosed — operation issued on a closed multistore.
var ErrMultistoreClosed = errors.New("scrinium: multistore closed")
