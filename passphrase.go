package scrinium

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/rkurbatov/scrinium/engine/core"
)

// loadPassphraseProvider returns a PassphraseProvider that
// reads from the file at path. The first prompt opens the
// file; subsequent prompts return the same bytes. The file
// is read fresh each time the provider is invoked because
// rotation (RotateKEK) may legitimately re-prompt and the
// host might have updated the file in between.
//
// Trailing newlines are stripped — a passphrase ending with
// "\n" or "\r\n" is the normal output of `echo "secret" >
// passphrase.txt`, and treating those bytes as part of the
// passphrase silently breaks Unlock against a Store
// initialised differently.
//
// Returns nil for an empty path. Hosts that want to remain
// Plain-DEK pass an empty PassphraseFile.
func loadPassphraseProvider(path string) (core.PassphraseProvider, error) {
	if path == "" {
		return nil, nil
	}
	// Stat first to surface a clear "not a file" error rather
	// than the ReadFile call's wrapped permission error.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("passphrase file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("passphrase file %q: is a directory", path)
	}

	return func(ctx context.Context, _ core.PassphraseHint) ([]byte, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("passphrase file: %w", err)
		}
		// Strip trailing CR/LF only — internal whitespace is
		// part of the passphrase. A single trailing newline is
		// the typical shell-redirect artifact; tolerate \n and
		// \r\n.
		raw = trimTrailingLineBreak(raw)
		if len(raw) == 0 {
			return nil, errors.New("passphrase file: empty after trim")
		}
		return raw, nil
	}, nil
}

func trimTrailingLineBreak(b []byte) []byte {
	if len(b) >= 2 && b[len(b)-2] == '\r' && b[len(b)-1] == '\n' {
		return b[:len(b)-2]
	}
	if len(b) >= 1 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		return b[:len(b)-1]
	}
	return b
}
