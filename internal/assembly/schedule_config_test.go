package assembly

import (
	decl "scrinium.dev/config/declarative"
	"strings"
	"testing"
	"time"
)

func TestApplyDefaults_FillsIntervalCadence(t *testing.T) {
	c := &decl.Config{Store: &decl.StoreSpec{Driver: "file:///x", Policy: &decl.Policy{
		GC:         &decl.Schedule{},
		Scrub:      &decl.ScrubSchedule{},
		Checkpoint: &decl.Schedule{},
	}}}
	if err := c.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	p := c.Store.Policy
	if p.GC.Every != decl.DefaultGCEvery || p.GC.Schedule != "" {
		t.Errorf("gc cadence = {%v, %q}, want default interval", p.GC.Every, p.GC.Schedule)
	}
	if p.Scrub.Every != decl.DefaultScrubEvery {
		t.Errorf("scrub every = %v, want %v", p.Scrub.Every, decl.DefaultScrubEvery)
	}
	if p.Checkpoint.Every != decl.DefaultCheckpointEvery {
		t.Errorf("checkpoint every = %v, want %v", p.Checkpoint.Every, decl.DefaultCheckpointEvery)
	}
}

func TestApplyDefaults_KeepsExplicitCron(t *testing.T) {
	c := &decl.Config{Store: &decl.StoreSpec{Driver: "file:///x", Policy: &decl.Policy{
		GC: &decl.Schedule{Schedule: "0 5 * * *"},
	}}}
	if err := c.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if p := c.Store.Policy; p.GC.Schedule != "0 5 * * *" || p.GC.Every != 0 {
		t.Errorf("explicit cron overwritten: {%v, %q}", p.GC.Every, p.GC.Schedule)
	}
}

func TestValidate_RejectsBothTriggers_GC(t *testing.T) {
	c := &decl.Config{Store: &decl.StoreSpec{Driver: "file:///x", Policy: &decl.Policy{
		GC: &decl.Schedule{Every: decl.Duration(time.Hour), Schedule: "0 3 * * *"},
	}}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("gc with every+schedule: err = %v, want one mentioning 'both'", err)
	}
}

func TestValidate_RejectsBothTriggers_Agent(t *testing.T) {
	c := &decl.Config{
		Store:  &decl.StoreSpec{Driver: "file:///x"},
		Agents: []decl.AgentSpec{{Kind: "x", Every: decl.Duration(time.Hour), Schedule: "0 3 * * *"}},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("agent with every+schedule: err = %v, want one mentioning 'both'", err)
	}
}

func TestValidate_RejectsNegativeInterval(t *testing.T) {
	c := &decl.Config{Store: &decl.StoreSpec{Driver: "file:///x", Policy: &decl.Policy{
		Scrub: &decl.ScrubSchedule{Every: decl.Duration(-time.Hour)},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("scrub with negative every = nil, want error")
	}
}

func TestValidate_AllowsSingleTrigger(t *testing.T) {
	// Interval only, and cron only, are both fine.
	for _, p := range []*decl.Policy{
		{GC: &decl.Schedule{Every: decl.Duration(time.Hour)}},
		{GC: &decl.Schedule{Schedule: "0 3 * * *"}},
	} {
		c := &decl.Config{Store: &decl.StoreSpec{Driver: "file:///x", Policy: p}}
		if err := c.Validate(); err != nil {
			t.Errorf("single trigger rejected: %v", err)
		}
	}
}
