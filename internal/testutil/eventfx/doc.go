// Package eventfx provides test helpers around event.EventBus —
// the most common need being a Recorder that captures every
// Publish call for assertion. Used by any package that emits
// events through event.EventBus and wants its tests to verify
// that emission happened (projection, agents, curator, etc.).
package eventfx
