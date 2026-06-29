// Package clog is the shared logging setup for the daemon binaries
// (scrinium-fuse, scrinium-webdav, scrinium-webview). It returns a
// *slog.Logger — the engine's standard logger type (ADR-60) — wired to a
// terminal-friendly, optionally coloured handler.
//
// Levels follow one rule: errors are always shown; per-operation traces
// (every WebDAV/WebView request, every FUSE mutation) are emitted at Debug
// and therefore appear only when the daemon runs with --debug (or
// SCRINIUM_DEBUG set). With debugging off the surfaces stay quiet except for
// genuine failures.
//
// Colour is on when stderr is a terminal and neither NO_COLOR nor
// SCRINIUM_NO_COLOR is set. The handler colours the level tag, HTTP status
// codes (2xx green … 5xx red), durations, and error values; everything else
// renders plain.
package clog

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// New builds the daemon logger. debug raises the level from Info to Debug,
// which is what turns the per-operation request/mutation traces on.
func New(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(newHandler(os.Stderr, level, useColor(os.Stderr)))
}

// EnvDebug reports whether SCRINIUM_DEBUG is set. Daemons use it as the
// default for their --debug flag so the env var keeps working.
func EnvDebug() bool { return os.Getenv("SCRINIUM_DEBUG") != "" }

func useColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("SCRINIUM_NO_COLOR") != "" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ANSI SGR codes.
const (
	cReset  = "\x1b[0m"
	cDim    = "\x1b[2m"
	cBold   = "\x1b[1m"
	cRed    = "\x1b[31m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cCyan   = "\x1b[36m"
	cGray   = "\x1b[90m"
)

// handler is a small slog.Handler that prints one line per record:
//
//	HH:MM:SS.mmm  LVL  message  key=val key=val
//
// It is intentionally minimal — a CLI surface, not structured ingestion —
// but implements WithAttrs/WithGroup correctly so library code that groups
// or pre-binds attributes still renders sensibly.
type handler struct {
	mu     *sync.Mutex
	w      io.Writer
	level  slog.Level
	color  bool
	prefix string      // accumulated group prefix, e.g. "req."
	attrs  []slog.Attr // pre-bound attrs, keys already group-qualified
}

func newHandler(w io.Writer, level slog.Level, color bool) *handler {
	return &handler{mu: &sync.Mutex{}, w: w, level: level, color: color}
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *handler) clone() *handler {
	c := *h
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	return &c
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	c := h.clone()
	for _, a := range as {
		a.Key = h.prefix + a.Key
		c.attrs = append(c.attrs, a)
	}
	return c
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := h.clone()
	c.prefix = h.prefix + name + "."
	return c
}

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	ts := r.Time.Format("15:04:05.000")
	b.WriteString(h.paint(cDim, ts))
	b.WriteByte(' ')
	b.WriteString(h.levelTag(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)

	for _, a := range h.attrs {
		h.writeAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		a.Key = h.prefix + a.Key
		h.writeAttr(&b, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *handler) writeAttr(b *strings.Builder, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	b.WriteByte(' ')
	b.WriteString(h.paint(cDim, a.Key+"="))
	b.WriteString(h.paintValue(a))
}

// paintValue colours a handful of well-known keys: status by HTTP class,
// errors red, durations dim. Other values render plain (quoted when they
// contain spaces so paths stay readable).
func (h *handler) paintValue(a slog.Attr) string {
	raw := a.Value.String()
	switch baseKey(a.Key) {
	case "status":
		if a.Value.Kind() == slog.KindInt64 {
			return h.paint(statusColor(int(a.Value.Int64())), raw)
		}
	case "err", "error":
		return h.paint(cRed, raw)
	case "dur", "duration", "took":
		return h.paint(cDim, raw)
	case "errno":
		return h.paint(cYellow, raw)
	}
	if strings.ContainsAny(raw, " \t") {
		return strconv.Quote(raw)
	}
	return raw
}

func (h *handler) levelTag(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return h.paint(cRed, "ERR")
	case l >= slog.LevelWarn:
		return h.paint(cYellow, "WRN")
	case l >= slog.LevelInfo:
		return h.paint(cCyan, "INF")
	default:
		return h.paint(cGray, "DBG")
	}
}

func (h *handler) paint(code, s string) string {
	if !h.color {
		return s
	}
	return code + s + cReset
}

func statusColor(code int) string {
	switch {
	case code >= 500:
		return cRed
	case code >= 400:
		return cYellow
	case code >= 300:
		return cCyan
	case code >= 200:
		return cGreen
	default:
		return cDim
	}
}

// baseKey strips any group prefix so the well-known-key switch matches
// whether or not the attr was emitted inside a group.
func baseKey(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// compile-time guard.
var _ slog.Handler = (*handler)(nil)
