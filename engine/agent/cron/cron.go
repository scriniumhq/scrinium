// Package cron enables cron-expression scheduling for Scrinium agents.
//
// It is activated by a call, not a blank import: passing cron.Enable() to
// scrinium.Open/Build wires a cron-expression parser (backed by
// robfig/cron/v3) into the assembly, after which the client's ScheduleCron
// accepts standard 5-field cron expressions and descriptors
// ("0 3 * * *", "@daily", "@every 1h"). Because activation is the call,
// the compiler enforces the import; the cron dependency is pulled in only
// by code that calls Enable, and the scheduler primitive itself stays
// cron-agnostic.
package cron

import (
	"time"

	robfig "github.com/robfig/cron/v3"

	"scrinium.dev/internal/assembly"
)

// Enable turns on cron-expression scheduling. Combine it with
// WithStandardScheduler so the schedules it accepts are actually ticked:
//
//	c, _ := scrinium.Open(ctx, uri, scrinium.WithStandardScheduler(), cron.Enable())
//	c.ScheduleCron("scrub", "0 3 * * *", scrub.ScrubConfig{})
//
// The returned option installs the parser into the build via the
// assembler's SPI; the call site's import of this package is what pulls
// in the cron dependency.
func Enable() assembly.BuildOption {
	return func(o *assembly.Options) { o.SetCronParser(parse) }
}

// parse implements agent.CronParser with robfig/cron/v3's standard parser
// (5-field expressions plus descriptors). The returned function reports
// the first scheduled moment strictly after prev — exactly the wall-clock
// gate the scheduler expects.
func parse(expr string) (func(prev time.Time) time.Time, error) {
	sched, err := robfig.ParseStandard(expr)
	if err != nil {
		return nil, err
	}
	return sched.Next, nil
}
