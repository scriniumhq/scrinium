package storefx

import (
	"context"
	"errors"

	"scrinium.dev/store"
)

// StaticPP is a one-line PassphraseProvider for tests: returns the
// same passphrase regardless of hint. Use when the test only needs
// one valid credential.
func StaticPP(pass string) store.PassphraseProvider {
	return func(_ context.Context, _ store.PassphraseHint) ([]byte, error) {
		return []byte(pass), nil
	}
}

// RecordingPP returns the configured passphrase but records every
// PassphraseHint it sees into log. Use when the test asserts on
// Reason / StoreID values that the engine threads through the
// provider call.
//
// log is appended to, not reset — pass a fresh slice per test.
func RecordingPP(pass string, log *[]store.PassphraseHint) store.PassphraseProvider {
	return func(_ context.Context, h store.PassphraseHint) ([]byte, error) {
		*log = append(*log, h)
		return []byte(pass), nil
	}
}

// ScriptedPP returns a different passphrase per call, driven by
// values. Use to script provider behaviour across two-call methods
// (RotateKEK invokes the provider twice — current then new). Returns
// an error after the script is exhausted.
func ScriptedPP(values ...string) store.PassphraseProvider {
	i := 0
	return func(_ context.Context, _ store.PassphraseHint) ([]byte, error) {
		if i >= len(values) {
			return nil, errors.New("storefx.ScriptedPP: script exhausted")
		}
		v := values[i]
		i++
		return []byte(v), nil
	}
}
