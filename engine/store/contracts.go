package store

import (
	"context"
	"hash"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/event"
)

// contracts.go — assorted engine plugin contracts that do not
// belong to the transformer or key-resolver families: the event
// Publisher, the MaintenanceAgent one-shot operation contract with
// its AgentResult, and the hash-registry constructor. Formerly
// plugins.go.

// Publisher is the minimal contract for emitting events; it is
// passed to Store via WithPublisher. It is satisfied by
// event.EventBus and by any custom implementation (asynchronous,
// persistent, filtering).
type Publisher interface {
	Publish(e event.Event)
}

// MaintenanceAgent is the contract of a one-shot administrative
// operation. Declared here (rather than in agent/) so that Store
// can require a MaintenanceAgent to be validated through Validate
// without depending on higher layers.
type MaintenanceAgent interface {
	// Validate checks whether the operation is applicable to the
	// current state of the Store: required maintenance mode,
	// presence of required parameters, availability of
	// dependencies.
	Validate(ctx context.Context) error

	// Run starts the operation. It acquires a maintenance/lease,
	// performs the work, and releases the lease. It returns the
	// result with accumulated statistics.
	Run(ctx context.Context) (*AgentResult, error)
}

// AgentResult is the result of an agent's work (one-shot or one
// background cycle). Used in EventAgentCompleted and
// EventAgentCycle.
type AgentResult struct {
	AgentType   string
	StoreID     string
	StartedAt   time.Time
	CompletedAt time.Time
	Stats       map[string]int64
	Partial     bool // true if the work was interrupted and completed only partially
}

// NewHashRegistry creates an empty hash-algorithm registry.
// The host application registers factories through Register.
func NewHashRegistry() domain.HashRegistry {
	return &hashRegistry{hashers: make(map[string]func() hash.Hash)}
}
