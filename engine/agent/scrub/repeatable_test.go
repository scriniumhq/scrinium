package scrub_test

import (
	"context"
	"testing"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/scrub"
)

func TestScrub_RunRepeatable(t *testing.T) {
	f := newScrubFixture(t)
	a := newScrub(t, f, scrub.ScrubConfig{Force: true})

	for i, label := range []string{"first", "second"} {
		res, err := a.Run(context.Background())
		if err != nil {
			t.Fatalf("%s Run: %v", label, err)
		}
		if res == nil || res.Partial {
			t.Errorf("%s Run: want non-nil non-Partial result, got %+v", label, res)
		}
		if st, _ := a.Status(); st != agent.StateIdle {
			t.Errorf("state after %s Run (i=%d) = %v, want StateIdle", label, i, st)
		}
	}
}
