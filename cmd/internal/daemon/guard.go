package daemon

import (
	"net/http"

	"scrinium.dev/domain"
)

// StoreGuard wraps next with a per-request maintenance check
// (ADR-111, INV-111-5): while the store is Offline every request is
// answered 503. Surfaces that serve listings and stats from the
// index/projection never touch the store gates — without this check a
// deleted (or substituted) physical store keeps being rendered from
// the index cache until restart. The mode func is a cheap RLock read
// (store.MaintenanceMode).
func StoreGuard(mode func() domain.MaintenanceMode, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode() == domain.MaintenanceModeOffline {
			http.Error(w,
				"store offline: physical store unreachable or in maintenance (ADR-111)",
				http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}
