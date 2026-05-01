package recoverykit

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/errs"
)

// Version is the kit format version emitted by Encode. Decode
// accepts only this version; future versions will arrive with a
// new const and a fork in Decode.
const Version = 1

// Header is the first line of every kit, used for fast format
// identification before parsing. Decode rejects input that does
// not start with this exact byte sequence.
const Header = "# Scrinium Recovery Kit v1"

// Kit is the in-memory representation of a Recovery Kit.
//
// The Encrypted field carries the DEK as it lives in the
// descriptor — wrapped by the KEK. recoverykit has no opinions
// on the wrap format; callers (core) compose Kit values from
// what keywrap.Wrap produced, and consume them by passing
// Kit.EncryptedDEK to keywrap.Unwrap.
type Kit struct {
	StoreID      string
	CreatedAt    time.Time
	Algorithm    string // KDF algorithm, e.g. "argon2id"
	Salt         []byte
	Time         uint32
	Memory       uint32
	Threads      uint8
	EncryptedDEK []byte
}

// Encode renders k as the canonical text form of a Recovery Kit.
// The trailing checksum line covers everything above it; decoders
// recompute the same hash and refuse mismatches.
//
// CreatedAt is normalised to UTC and formatted as RFC 3339; the
// caller may pass any zone, the on-disk form is always UTC for
// portability.
func Encode(k Kit) ([]byte, error) {
	if err := k.validate(); err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintln(&b, Header)
	fmt.Fprintf(&b, "# Generated: %s\n", k.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "[STORE]")
	fmt.Fprintf(&b, "StoreID    = %s\n", k.StoreID)
	fmt.Fprintf(&b, "CreatedAt  = %s\n", k.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "[KDF]")
	fmt.Fprintf(&b, "Algo    = %s\n", k.Algorithm)
	fmt.Fprintf(&b, "Salt    = %s\n", base64.StdEncoding.EncodeToString(k.Salt))
	fmt.Fprintf(&b, "Time    = %d\n", k.Time)
	fmt.Fprintf(&b, "Memory  = %d\n", k.Memory)
	fmt.Fprintf(&b, "Threads = %d\n", k.Threads)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "[KEY]")
	fmt.Fprintf(&b, "EncryptedDEK = %s\n",
		base64.StdEncoding.EncodeToString(k.EncryptedDEK))
	fmt.Fprintln(&b)

	// Checksum covers everything above the [CHECKSUM] line. We
	// hash the bytes accumulated so far, then append the checksum
	// section.
	body := b.String()
	sum := sha256.Sum256([]byte(body))

	fmt.Fprintln(&b, "[CHECKSUM]")
	fmt.Fprintf(&b, "Hash = sha256-%s\n", hex.EncodeToString(sum[:]))

	return []byte(b.String()), nil
}

// Decode parses a Recovery Kit. It returns errs.ErrRecoveryKitCorrupted
// for any failure in either the structural pass (missing
// section, bad header, malformed key=value) or the checksum pass
// (computed hash does not match the stored one). The wrapped error
// carries a concrete reason for diagnostic logging; callers
// should branch on errs.ErrRecoveryKitCorrupted.
func Decode(data []byte) (Kit, error) {
	checksumIdx := bytes.Index(data, []byte("\n[CHECKSUM]\n"))
	if checksumIdx < 0 {
		// Edge case: kit ends without a trailing newline before
		// [CHECKSUM]; tolerate by also looking for the section
		// header at any line boundary.
		checksumIdx := bytes.Index(data, []byte("\n[CHECKSUM]\n"))
		if checksumIdx <= 0 {
			return Kit{}, fmt.Errorf("%w: missing [CHECKSUM] section",
				errs.ErrRecoveryKitCorrupted)
		}
	} else {
		checksumIdx++ // include the leading newline in the body
	}

	body := data[:checksumIdx]
	tail := data[checksumIdx:]

	// Verify checksum FIRST — if the body has been tampered with,
	// any structural parsing of it produces nonsense, so refuse
	// before bothering.
	if err := verifyChecksum(body, tail); err != nil {
		return Kit{}, err
	}

	return parseBody(body)
}

// --- internals ---

func verifyChecksum(body, tail []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(tail))
	var got string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "[CHECKSUM]" {
			continue
		}
		k, v, ok := splitKV(line)
		if !ok {
			return fmt.Errorf("%w: malformed line in [CHECKSUM]: %q",
				errs.ErrRecoveryKitCorrupted, line)
		}
		if k != "Hash" {
			return fmt.Errorf("%w: unknown key %q in [CHECKSUM]",
				errs.ErrRecoveryKitCorrupted, k)
		}
		got = v
	}
	if got == "" {
		return fmt.Errorf("%w: missing Hash in [CHECKSUM]",
			errs.ErrRecoveryKitCorrupted)
	}

	const prefix = "sha256-"
	if !strings.HasPrefix(got, prefix) {
		return fmt.Errorf("%w: unsupported hash algo in %q",
			errs.ErrRecoveryKitCorrupted, got)
	}
	want, err := hex.DecodeString(strings.TrimPrefix(got, prefix))
	if err != nil {
		return fmt.Errorf("%w: hash hex decode: %v",
			errs.ErrRecoveryKitCorrupted, err)
	}

	have := sha256.Sum256(body)
	if !bytes.Equal(have[:], want) {
		return fmt.Errorf("%w: checksum mismatch",
			errs.ErrRecoveryKitCorrupted)
	}
	return nil
}

func parseBody(body []byte) (Kit, error) {
	s := bufio.NewScanner(bytes.NewReader(body))

	// Header line first.
	if !s.Scan() {
		return Kit{}, fmt.Errorf("%w: empty input",
			errs.ErrRecoveryKitCorrupted)
	}
	if h := strings.TrimRight(s.Text(), " \t\r"); h != Header {
		return Kit{}, fmt.Errorf("%w: bad header %q (want %q)",
			errs.ErrRecoveryKitCorrupted, h, Header)
	}

	var k Kit
	var section string

	for s.Scan() {
		line := strings.TrimRight(s.Text(), " \t\r")
		trimmed := strings.TrimSpace(line)

		// Skip blanks and comments.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Section header.
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = trimmed
			continue
		}

		// Key=value within a section.
		key, val, ok := splitKV(trimmed)
		if !ok {
			return Kit{}, fmt.Errorf("%w: malformed line %q",
				errs.ErrRecoveryKitCorrupted, trimmed)
		}

		if err := assignField(&k, section, key, val); err != nil {
			return Kit{}, err
		}
	}
	if err := s.Err(); err != nil {
		return Kit{}, fmt.Errorf("%w: scan: %v",
			errs.ErrRecoveryKitCorrupted, err)
	}

	if err := k.validate(); err != nil {
		return Kit{}, fmt.Errorf("%w: %v",
			errs.ErrRecoveryKitCorrupted, err)
	}
	return k, nil
}

func assignField(k *Kit, section, key, val string) error {
	corrupt := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{errs.ErrRecoveryKitCorrupted}, args...)...)
	}

	switch section {
	case "[STORE]":
		switch key {
		case "StoreID":
			k.StoreID = val
		case "CreatedAt":
			t, err := time.Parse(time.RFC3339, val)
			if err != nil {
				return corrupt("CreatedAt %q: %v", val, err)
			}
			k.CreatedAt = t
		default:
			return corrupt("unknown key %q in [STORE]", key)
		}
	case "[KDF]":
		switch key {
		case "Algo":
			k.Algorithm = val
		case "Salt":
			b, err := base64.StdEncoding.DecodeString(val)
			if err != nil {
				return corrupt("Salt base64: %v", err)
			}
			k.Salt = b
		case "Time":
			n, err := strconv.ParseUint(val, 10, 32)
			if err != nil {
				return corrupt("Time %q: %v", val, err)
			}
			k.Time = uint32(n)
		case "Memory":
			n, err := strconv.ParseUint(val, 10, 32)
			if err != nil {
				return corrupt("Memory %q: %v", val, err)
			}
			k.Memory = uint32(n)
		case "Threads":
			n, err := strconv.ParseUint(val, 10, 8)
			if err != nil {
				return corrupt("Threads %q: %v", val, err)
			}
			k.Threads = uint8(n)
		default:
			return corrupt("unknown key %q in [KDF]", key)
		}
	case "[KEY]":
		switch key {
		case "EncryptedDEK":
			b, err := base64.StdEncoding.DecodeString(val)
			if err != nil {
				return corrupt("EncryptedDEK base64: %v", err)
			}
			k.EncryptedDEK = b
		default:
			return corrupt("unknown key %q in [KEY]", key)
		}
	case "":
		return corrupt("key=value line %q outside any section", key)
	default:
		return corrupt("unknown section %q", section)
	}
	return nil
}

func splitKV(line string) (key, val string, ok bool) {
	i := strings.IndexRune(line, '=')
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	val = strings.TrimSpace(line[i+1:])
	return key, val, true
}

// validate checks that every required field is populated.
// Encode calls it to refuse rendering an incomplete kit;
// Decode calls it after parsing to catch missing sections
// (which would otherwise leave fields zero-valued).
func (k Kit) validate() error {
	missing := func(what string) error {
		return fmt.Errorf("incomplete kit: missing %s", what)
	}
	switch {
	case k.StoreID == "":
		return missing("StoreID")
	case k.CreatedAt.IsZero():
		return missing("CreatedAt")
	case k.Algorithm == "":
		return missing("Algorithm")
	case len(k.Salt) == 0:
		return missing("Salt")
	case k.Time == 0:
		return missing("Time")
	case k.Memory == 0:
		return missing("Memory")
	case k.Threads == 0:
		return missing("Threads")
	case len(k.EncryptedDEK) == 0:
		return missing("EncryptedDEK")
	}
	return nil
}
