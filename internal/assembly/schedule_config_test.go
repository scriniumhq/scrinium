package assembly

import (
	"strings"
	"testing"
	"time"
)

func TestApplyDefaults_FillsIntervalCadence(t *testing.T) {
	c := &Config{Store: &StoreSpec{Driver: "file:///x", Policy: &Policy{
		GC:         &Schedule{},
		Scrub:      &ScrubSchedule{},
		Checkpoint: &Schedule{},
	}}}
	applyDefaults(c)
	p := c.Store.Policy
	if p.GC.Every != defaultGCEvery || p.GC.Schedule != "" {
		t.Errorf("gc cadence = {%v, %q}, want default interval", p.GC.Every, p.GC.Schedule)
	}
	if p.Scrub.Every != defaultScrubEvery {
		t.Errorf("scrub every = %v, want %v", p.Scrub.Every, defaultScrubEvery)
	}
	if p.Checkpoint.Every != defaultCheckpointEvery {
		t.Errorf("checkpoint every = %v, want %v", p.Checkpoint.Every, defaultCheckpointEvery)
	}
}

func TestApplyDefaults_KeepsExplicitCron(t *testing.T) {
	c := &Config{Store: &StoreSpec{Driver: "file:///x", Policy: &Policy{
		GC: &Schedule{Schedule: "0 5 * * *"},
	}}}
	applyDefaults(c)
	if p := c.Store.Policy; p.GC.Schedule != "0 5 * * *" || p.GC.Every != 0 {
		t.Errorf("explicit cron overwritten: {%v, %q}", p.GC.Every, p.GC.Schedule)
	}
}

func TestValidate_RejectsBothTriggers_GC(t *testing.T) {
	c := &Config{Store: &StoreSpec{Driver: "file:///x", Policy: &Policy{
		GC: &Schedule{Every: Duration(time.Hour), Schedule: "0 3 * * *"},
	}}}
	err := validate(c)
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("gc with every+schedule: err = %v, want one mentioning 'both'", err)
	}
}

func TestValidate_RejectsBothTriggers_Agent(t *testing.T) {
	c := &Config{
		Store:  &StoreSpec{Driver: "file:///x"},
		Agents: []ComponentSpec{{Kind: "x", Every: Duration(time.Hour), Schedule: "0 3 * * *"}},
	}
	err := validate(c)
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("agent with every+schedule: err = %v, want one mentioning 'both'", err)
	}
}

func TestValidate_RejectsNegativeInterval(t *testing.T) {
	c := &Config{Store: &StoreSpec{Driver: "file:///x", Policy: &Policy{
		Scrub: &ScrubSchedule{Every: Duration(-time.Hour)},
	}}}
	if err := validate(c); err == nil {
		t.Error("scrub with negative every = nil, want error")
	}
}

func TestValidate_AllowsSingleTrigger(t *testing.T) {
	// Interval only, and cron only, are both fine.
	for _, p := range []*Policy{
		{GC: &Schedule{Every: Duration(time.Hour)}},
		{GC: &Schedule{Schedule: "0 3 * * *"}},
	} {
		c := &Config{Store: &StoreSpec{Driver: "file:///x", Policy: p}}
		if err := validate(c); err != nil {
			t.Errorf("single trigger rejected: %v", err)
		}
	}
}
