package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"scrinium.dev/domain"
)

// E2E for the per-request maintenance check (ADR-111, INV-111-5): the
// exact webview failure mode — a handler serving from the index keeps
// answering after the physical store is gone — must be cut off by the
// guard as soon as the sentinel flips the mode.
func TestStoreGuard(t *testing.T) {
	mode := domain.MaintenanceModeNone
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("listing"))
	})
	h := StoreGuard(func() domain.MaintenanceMode { return mode }, inner)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/by-path/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("online: got %d, want 200", rr.Code)
	}

	mode = domain.MaintenanceModeOffline
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/by-path/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline: got %d, want 503", rr.Code)
	}

	mode = domain.MaintenanceModeNone
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/by-path/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("back online: got %d, want 200", rr.Code)
	}
}
