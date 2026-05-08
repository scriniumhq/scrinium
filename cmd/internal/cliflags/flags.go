// Package cliflags provides flag.Value implementations shared
// across the scrinium-fuse, scrinium-webdav, and scrinium-webview
// binaries. Each cmd binary still owns its own bindFlags — what
// is exposed differs per surface (webview is read-only, fuse
// adds mount-specific flags, etc.) — but the underlying flag
// types are common.
//
// Lives under cmd/internal/ because the consumers are exactly
// the cmd/ binaries and nothing else; sibling to cmd/internal/
// daemon, which follows the same pattern.
package cliflags

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rkurbatov/scrinium/projection"
)

// RootViewFlag binds a CLI flag to *projection.RootView with
// allowed-value validation: by-path | by-session | by-namespace
// | by-date | by-artifact.
type RootViewFlag struct{ P *projection.RootView }

func (f RootViewFlag) String() string {
	if f.P == nil {
		return ""
	}
	return string(*f.P)
}

func (f RootViewFlag) Set(s string) error {
	rv := projection.RootView(s)
	switch rv {
	case projection.RootByPath, projection.RootBySession,
		projection.RootByNamespace, projection.RootByDate, projection.RootByArtifact:
		*f.P = rv
		return nil
	}
	return fmt.Errorf("invalid root-view %q", s)
}

// BoolPtrFlag binds a CLI flag to **bool — nil means "not set",
// allowing the editing-custom logic to distinguish "default"
// from "explicit false".
type BoolPtrFlag struct{ P **bool }

func (f BoolPtrFlag) String() string {
	if f.P == nil || *f.P == nil {
		return ""
	}
	return strconv.FormatBool(**f.P)
}

func (f BoolPtrFlag) Set(s string) error {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	*f.P = &b
	return nil
}

// IsBoolFlag tells the flag package the flag accepts no
// argument when written as -flag (sets to true).
func (f BoolPtrFlag) IsBoolFlag() bool { return true }

// ByteSizeFlag accepts human-friendly suffixes on integer
// byte counts: "500", "500K", "500M", "1G", "2T". Lower- and
// upper-case suffixes are equivalent. Binary multipliers
// (1K = 1024).
type ByteSizeFlag struct{ P *int64 }

func (f ByteSizeFlag) String() string {
	if f.P == nil {
		return ""
	}
	return strconv.FormatInt(*f.P, 10)
}

func (f ByteSizeFlag) Set(s string) error {
	if s == "" {
		return fmt.Errorf("empty size")
	}
	mult := int64(1)
	last := s[len(s)-1]
	switch last {
	case 'k', 'K':
		mult = 1 << 10
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 1 << 20
		s = s[:len(s)-1]
	case 'g', 'G':
		mult = 1 << 30
		s = s[:len(s)-1]
	case 't', 'T':
		mult = 1 << 40
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return fmt.Errorf("size: %w", err)
	}
	*f.P = n * mult
	return nil
}

// OctalFlag accepts octal POSIX-style mode strings like "0644"
// or "644" (leading 0 optional).
type OctalFlag struct{ P *uint32 }

func (f OctalFlag) String() string {
	if f.P == nil {
		return ""
	}
	return fmt.Sprintf("0%o", *f.P)
}

func (f OctalFlag) Set(s string) error {
	s = strings.TrimPrefix(s, "0o")
	s = strings.TrimPrefix(s, "0")
	if s == "" {
		*f.P = 0
		return nil
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return fmt.Errorf("octal mode: %w", err)
	}
	*f.P = uint32(n)
	return nil
}

// UintFlag binds a CLI flag to *uint32.
type UintFlag struct{ P *uint32 }

func (f UintFlag) String() string {
	if f.P == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*f.P), 10)
}

func (f UintFlag) Set(s string) error {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return err
	}
	*f.P = uint32(n)
	return nil
}
