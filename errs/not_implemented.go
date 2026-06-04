package errs

import "errors"

// ErrNotImplemented signals that a method or branch is part of the
// declared public surface but its implementation has not landed yet.
// Distinct from forward-compat sentinels (ErrUnsupportedEncoding,
// ErrUnsupportedCrypto), which mark "this binary does not support
// that format" — ErrNotImplemented says "no binary supports this
// path yet".
//
// Returned (wrapped, with concrete context) by stub bodies of types
// that exist for compile-time DAG enforcement: agents, the
// projection layer, chunker / bundler decorators — anywhere the M0
// contract has a signature but the flesh arrives at M3 or later.
//
// Tests that exercise stubs match against this with errors.Is.
var ErrNotImplemented = errors.New("scrinium: not implemented")
