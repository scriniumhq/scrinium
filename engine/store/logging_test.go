package store

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store/internal/crypto"
)

// --- capturing handler ---------------------------------------------------
//
// captureHandler accumulates slog.Records (with their group/attr context
// resolved into a flat map) so tests can assert what was logged, at what
// level, with which attributes — the slog analogue of zaptest/observer.

type captured struct {
	Level   slog.Level
	Message string
	Attrs   map[string]string
}

type captureHandler struct {
	mu      *sync.Mutex
	records *[]captured
	level   slog.Level
	groups  []string
	attrs   []slog.Attr
}

func newCaptureHandler(level slog.Level) (*captureHandler, *[]captured, *sync.Mutex) {
	recs := &[]captured{}
	mu := &sync.Mutex{}
	return &captureHandler{mu: mu, records: recs, level: level}, recs, mu
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *captureHandler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.attrs = append(append([]slog.Attr{}, h.attrs...), prefixAttrs(h.groups, as)...)
	return &n
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	n := *h
	n.groups = append(append([]string{}, h.groups...), name)
	return &n
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	flat := map[string]string{}
	for _, a := range h.attrs {
		flat[a.Key] = a.Value.Resolve().String()
	}
	prefix := ""
	if len(h.groups) > 0 {
		prefix = joinGroups(h.groups) + "."
	}
	r.Attrs(func(a slog.Attr) bool {
		flat[prefix+a.Key] = a.Value.Resolve().String()
		return true
	})
	h.mu.Lock()
	*h.records = append(*h.records, captured{Level: r.Level, Message: r.Message, Attrs: flat})
	h.mu.Unlock()
	return nil
}

func prefixAttrs(groups []string, as []slog.Attr) []slog.Attr {
	if len(groups) == 0 {
		return as
	}
	p := joinGroups(groups) + "."
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = slog.String(p+a.Key, a.Value.Resolve().String())
	}
	return out
}

func joinGroups(groups []string) string {
	out := ""
	for i, g := range groups {
		if i > 0 {
			out += "."
		}
		out += g
	}
	return out
}

// --- default silence -----------------------------------------------------

func TestResolveLogger_NilIsSilent(t *testing.T) {
	l := resolveLogger(nil)
	if l == nil {
		t.Fatal("resolveLogger(nil) returned nil; must return a usable discard logger")
	}
	if l.Enabled(context.Background(), slog.LevelError) {
		t.Error("discard logger reports Enabled==true; must be silent at every level")
	}
}

func TestStoreLogger_NilFieldIsSilent(t *testing.T) {
	s := &store{} // log field left nil, as a hand-built test store
	if s.logger() == nil {
		t.Fatal("logger() returned nil; must substitute the discard logger")
	}
	if s.logger().Enabled(context.Background(), slog.LevelDebug) {
		t.Error("nil-logger store is not silent")
	}
}

// --- namespacing / sublogger ---------------------------------------------

func TestResolveLogger_AddsScriniumGroup(t *testing.T) {
	h, recs, _ := newCaptureHandler(slog.LevelDebug)
	s := &store{storeID: "store-123", log: resolveLogger(slog.New(h))}

	s.componentLogger("gc").Info("hello", slog.String("k", "v"))

	if len(*recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(*recs))
	}
	got := (*recs)[0]
	// component attr and the call attr are nested under the "scrinium" group.
	if got.Attrs["scrinium.component"] != "gc" {
		t.Errorf("component attr: want scrinium.component=gc, got %q", got.Attrs["scrinium.component"])
	}
	if got.Attrs["scrinium.k"] != "v" {
		t.Errorf("call attr: want scrinium.k=v, got %q", got.Attrs["scrinium.k"])
	}
}

// --- redaction (security-critical) ---------------------------------------

func TestRedaction_DEKNeverRendered(t *testing.T) {
	secret := []byte("super-secret-dek-bytes")
	v := redactedSecret(secret).LogValue()
	if got := v.String(); got != redactedKey {
		t.Fatalf("redactedSecret rendered %q; must be %q", got, redactedKey)
	}
	if bytes.Contains([]byte(v.String()), secret) {
		t.Fatal("redactedSecret leaked key material into the log value")
	}
}

func TestRedaction_PassphraseNeverRendered(t *testing.T) {
	secret := []byte("correct horse battery staple")
	v := redactedSecret(secret).LogValue()
	if got := v.String(); got != redactedKey {
		t.Fatalf("redactedSecret rendered %q; must be %q", got, redactedKey)
	}
}

// TestRedaction_ThroughHandler proves the LogValuer is honoured by the
// logging pipeline end to end: even logging the whole secret value as an
// attribute spills only the sentinel.
func TestRedaction_ThroughHandler(t *testing.T) {
	h, recs, _ := newCaptureHandler(slog.LevelDebug)
	l := slog.New(h)

	l.Info("crypto op", slog.Any("dek", redactedSecret([]byte("leak-me-if-you-can"))))

	got := (*recs)[0].Attrs["dek"]
	if got != redactedKey {
		t.Fatalf("DEK attr rendered %q through the handler; must be %q", got, redactedKey)
	}
}

// --- safe attrs ----------------------------------------------------------

func TestKeyIDAttr_EmptyIsVisibleNotOmitted(t *testing.T) {
	a := keyIDAttr("")
	if a.Key != "key_id" {
		t.Errorf("key: want key_id, got %q", a.Key)
	}
	if a.Value.String() != "" {
		t.Errorf("empty KeyID should render empty, got %q", a.Value.String())
	}
}

// --- Close emits a lock-free trace ---------------------------------------

func TestClose_EmitsDebugTrace(t *testing.T) {
	h, recs, _ := newCaptureHandler(slog.LevelDebug)
	s := &store{
		storeID: "store-xyz",
		state:   domain.StateUnlocked,
		log:     resolveLogger(slog.New(h)),
		crypto:  crypto.New(nil, []byte{1, 2, 3, 4}, nil, nil, nil, nil),
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var found *captured
	for i := range *recs {
		if (*recs)[i].Message == "store closed" {
			found = &(*recs)[i]
			break
		}
	}
	if found == nil {
		t.Fatal(`no "store closed" record emitted by Close`)
	}
	if found.Level != slog.LevelDebug {
		t.Errorf("level: want Debug, got %v", found.Level)
	}
	if found.Attrs["scrinium.store_id"] != "store-xyz" {
		t.Errorf("store_id attr: want store-xyz, got %q", found.Attrs["scrinium.store_id"])
	}
}

// TestClose_SilentByDefault ensures a store built without a logger emits
// nothing on Close (no panic, no output).
func TestClose_SilentByDefault(t *testing.T) {
	s := &store{state: domain.StateUnlocked, crypto: crypto.New(nil, []byte{9}, nil, nil, nil, nil)}
	if err := s.Close(); err != nil {
		t.Fatalf("Close on silent store: %v", err)
	}
}

// --- maintenance-mode attribute (enum → name, numeric fallback) ----------

func TestMaintenanceModeAttr_NamesKnownModes(t *testing.T) {
	cases := []struct {
		mode domain.MaintenanceMode
		want string
	}{
		{domain.MaintenanceModeNone, "none"},
		{domain.MaintenanceModeReadOnly, "read_only"},
		{domain.MaintenanceModeOffline, "offline"},
	}
	for _, tc := range cases {
		a := maintenanceModeAttr(tc.mode)
		if a.Key != "mode" {
			t.Errorf("%v: key = %q, want \"mode\"", tc.mode, a.Key)
		}
		if a.Value.Kind() != slog.KindString || a.Value.String() != tc.want {
			t.Errorf("%v: rendered %v(%q), want string %q", tc.mode, a.Value.Kind(), a.Value.String(), tc.want)
		}
	}
}

func TestMaintenanceModeAttr_UnknownFallsBackToNumeric(t *testing.T) {
	// A mode the switch does not name must still log something meaningful
	// — the numeric value — so a future MaintenanceMode addition stays
	// visible in the record instead of being silently dropped.
	const unknown = domain.MaintenanceMode(99)
	a := maintenanceModeAttr(unknown)
	if a.Key != "mode" {
		t.Errorf("key = %q, want \"mode\"", a.Key)
	}
	if a.Value.Kind() != slog.KindInt64 {
		t.Fatalf("unknown mode rendered as %v; want numeric (KindInt64)", a.Value.Kind())
	}
	if a.Value.Int64() != int64(unknown) {
		t.Errorf("numeric value = %d, want %d", a.Value.Int64(), int64(unknown))
	}
}

// --- construction-phase logger (Init/Open, before a *store exists) -------

func TestOptsLogger_NilLoggerIsSilent(t *testing.T) {
	l := optsLogger(storeOptions{}, "init") // logger field left nil
	if l == nil {
		t.Fatal("optsLogger returned nil; the construction path must be unconditionally loggable")
	}
	if l.Enabled(context.Background(), slog.LevelError) {
		t.Error("nil-logger construction phase is not silent")
	}
}

func TestOptsLogger_NamespacesAndTagsComponent(t *testing.T) {
	h, recs, _ := newCaptureHandler(slog.LevelDebug)
	l := optsLogger(storeOptions{logger: slog.New(h)}, "init")

	l.Info("bootstrapping", slog.String("k", "v"))

	if len(*recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(*recs))
	}
	got := (*recs)[0]
	// Component tag and call attr both sit under "scrinium", exactly what
	// buildStore installs on the live store — optsLogger must match it so
	// construction-phase records and steady-state records read alike.
	if got.Attrs["scrinium.component"] != "init" {
		t.Errorf("component attr: want scrinium.component=init, got %q", got.Attrs["scrinium.component"])
	}
	if got.Attrs["scrinium.k"] != "v" {
		t.Errorf("call attr: want scrinium.k=v, got %q", got.Attrs["scrinium.k"])
	}
}
