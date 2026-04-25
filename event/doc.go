// Package event provides shared event types and the event bus
// implementation used by all Scrinium layers.
//
// This is a leaf package in the dependency DAG: it does not import
// anything from the project except the standard library. Placing it
// here, rather than inside curator, allows the minimal stack
// (a single Store without Curator) to subscribe to and publish events.
//
// The default EventBus implementation is synchronous, panic-safe, and
// non-persistent. Custom implementations (asynchronous, buffered,
// filtering) are the host application's responsibility and plug in
// through the Publisher interface declared in core.
package event
