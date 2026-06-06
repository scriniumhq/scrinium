package assembly

import "testing"

func TestOptions_WithSchedule(t *testing.T) {
	var o Options
	WithSchedule("scrub", "0 3 * * *")(&o)
	WithSchedule("gc", "6h")(&o)

	if got := o.schedules["scrub"]; got != "0 3 * * *" {
		t.Errorf("scrub schedule = %q, want cron", got)
	}
	if got := o.schedules["gc"]; got != "6h" {
		t.Errorf("gc schedule = %q, want interval", got)
	}

	// Replace-by-kind: a repeat for the same kind wins, no second entry.
	WithSchedule("scrub", "0 4 * * 0")(&o)
	if got := o.schedules["scrub"]; got != "0 4 * * 0" {
		t.Errorf("scrub schedule after replace = %q, want last value", got)
	}
	if len(o.schedules) != 2 {
		t.Errorf("schedules len = %d, want 2 (replace, not append)", len(o.schedules))
	}

	// Empty kind or expr is ignored.
	WithSchedule("", "6h")(&o)
	WithSchedule("x", "")(&o)
	if len(o.schedules) != 2 {
		t.Errorf("empty kind/expr changed schedules: len = %d, want 2", len(o.schedules))
	}
}

func TestOptions_WithAgentConfig(t *testing.T) {
	type gcCfg struct{ Grace int }
	var o Options
	WithAgentConfig("gc", gcCfg{Grace: 24})(&o)
	WithAgentConfig("gc", gcCfg{Grace: 48})(&o) // replace-by-kind

	got, ok := o.agentConfigs["gc"].(gcCfg)
	if !ok || got.Grace != 48 {
		t.Errorf("gc config = %+v (ok=%v), want Grace=48", o.agentConfigs["gc"], ok)
	}
	if len(o.agentConfigs) != 1 {
		t.Errorf("agentConfigs len = %d, want 1 (replace, not append)", len(o.agentConfigs))
	}

	// Empty kind is ignored.
	WithAgentConfig("", nil)(&o)
	if len(o.agentConfigs) != 1 {
		t.Errorf("empty kind changed agentConfigs: len = %d, want 1", len(o.agentConfigs))
	}
}
