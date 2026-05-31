package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"scrinium.dev/errs"
)

// uncertainErr models a failure whose request was sent but whose
// response was lost: transient (the cause is temporary) yet not safe
// to blindly retry (the write may have landed).
type uncertainErr struct{}

func (uncertainErr) Error() string   { return "uncertain" }
func (uncertainErr) Transient() bool { return true }
func (uncertainErr) Retriable() bool { return false }

// forcedRetriable opts into retriability without being one of the
// known transient sentinels.
type forcedRetriable struct{}

func (forcedRetriable) Error() string   { return "forced" }
func (forcedRetriable) Retriable() bool { return true }

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"source-unavailable", errs.ErrSourceUnavailable, true},
		{"lease-held", errs.ErrLeaseHeld, true},
		{"lease-lost", errs.ErrLeaseLost, true},
		{"maintenance", errs.ErrMaintenanceInProgress, true},
		{"not-ready", errs.ErrStoreNotReady, true},
		{"offline", errs.ErrStoreOffline, true},
		{"wrapped-transient", fmt.Errorf("dial: %w", errs.ErrLeaseHeld), true},
		{"terminal-not-found", errs.ErrArtifactNotFound, false},
		{"denied-read-only", errs.ErrStoreReadOnly, false},
		{"locked", errs.ErrLocked, false},
		{"marker-uncertain", uncertainErr{}, true},
		{"plain", errors.New("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := errs.IsTransient(c.err); got != c.want {
				t.Fatalf("IsTransient(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestIsRetriable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"transient-sentinel", errs.ErrLeaseHeld, true},
		{"wrapped-transient", fmt.Errorf("x: %w", errs.ErrMaintenanceInProgress), true},
		{"terminal", errs.ErrArtifactNotFound, false},
		{"uncertain-opts-out", uncertainErr{}, false},
		{"wrapped-uncertain", fmt.Errorf("rpc: %w", uncertainErr{}), false},
		{"forced-retriable", forcedRetriable{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := errs.IsRetriable(c.err); got != c.want {
				t.Fatalf("IsRetriable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// Every transient sentinel must, by default, also be retriable (none
// of the current ones is uncertain).
func TestTransientSentinelsAreRetriable(t *testing.T) {
	for _, s := range []error{
		errs.ErrSourceUnavailable, errs.ErrLeaseHeld, errs.ErrLeaseLost,
		errs.ErrMaintenanceInProgress, errs.ErrStoreNotReady, errs.ErrStoreOffline,
	} {
		if !errs.IsRetriable(s) {
			t.Errorf("transient sentinel %v is not retriable", s)
		}
	}
}
