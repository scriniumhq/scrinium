package errs

import "errors"

// Temporary sentinels — errors that gate features deferred to a
// later milestone. Each one carries a comment with WHEN it goes
// away and WHY it exists in the meantime. When the gating feature
// lands, delete the sentinel from this file (and its callsites);
// no graceful deprecation cycle.
//
// This file is the engine's TODO list at the type level. Anything
// here should be reviewed at every milestone boundary: if the
// blocking work has shipped, the sentinel must go.

// ErrUnsupportedEncoding — manifestcodec was asked for
// ManifestEncoding: Binary (MsgPack, magic \x00SC2). Decoding
// returns the same sentinel when it sees binary magic on disk.
//
// Goes away when M2 lands the binary codec. Tracking: the bare
// magic check in internal/manifestcodec/codec.go and the matching
// test cases in codec_test.go.
var ErrUnsupportedEncoding = errors.New("scrinium: manifest encoding deferred to M2")

// ErrUnsupportedCrypto — manifestcodec was asked for any
// ManifestCrypto value other than Plain (MetadataOnly, Envelope).
// Decoding returns this when the file's crypto flag is non-zero.
//
// Goes away when M2 lands the crypto pipeline. Same review point
// as ErrUnsupportedEncoding above.
var ErrUnsupportedCrypto = errors.New("scrinium: manifest crypto deferred to M2")
