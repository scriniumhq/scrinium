package errs

import "errors"

// ErrInvalidSystemName — a SystemStore name violated the validation
// contract: empty, leading or trailing slash, empty segment ("//"),
// or a "." / ".." traversal segment.
var ErrInvalidSystemName = errors.New("scrinium: invalid system name")
