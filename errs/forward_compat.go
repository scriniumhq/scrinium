package errs

import "errors"

// Forward-compatibility sentinels for unrecognised manifest
// formats. Returned by engine/artifact when the header on
// disk or the StoreConfig in memory specifies an encoding or
// crypto mode the running binary does not know how to handle.
//
// Typical sources: a manifest written by a newer Scrinium
// version, a foreign file with Scrinium magic bytes, a config
// exported from a build with a feature flag the current binary
// lacks.
//
// These are part of the public error surface; matching against
// them with errors.Is is the supported way to distinguish "format
// from the future" from "file is broken". They are not gated on
// any particular milestone — every release will recognise some
// formats and not others, and these sentinels mark the boundary.

// ErrUnsupportedEncoding — the manifest header carries an
// encoding magic (\x00SC1\x00 for JSON, \x00SC2\x00 for the binary
// codec, etc.) that this binary was not built to decode, or
// StoreConfig.ManifestEncoding names a value not registered in
// the current build.
var ErrUnsupportedEncoding = errors.New("scrinium: unsupported manifest encoding")

// ErrUnsupportedCrypto — the manifest header carries a
// ManifestCrypto value (Plain, Sealed, Paranoid, or a
// future addition) that this binary does not implement, or
// StoreConfig.ManifestCrypto names such a value.
var ErrUnsupportedCrypto = errors.New("scrinium: unsupported manifest crypto")
