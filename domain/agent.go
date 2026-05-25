package domain

import (
	"context"
	"time"
)

// MaintenanceAgent is the contract of a one-shot administrative
// operation (rebuild index, migrate schema, snapshot index, …).
//
// It lives in domain — the leaf package every layer imports — because
// it is shared vocabulary between the layer that *runs* an agent
// (store.AdminStore.RunMaintenance names it in its signature) and the
// layers that *implement* one (engine/agent and engine/maintenance,
// both of which import store and so could not host a type store must
// name without forming an import cycle). domain already carries the
// other cross-cutting contracts (ReadHandle, HashRegistry) and the
// MaintenanceMode this contract pairs with, so it is the natural home.
// See ADR-42.
//
// The agent owns its own maintenance lease and event emission: Run
// acquires the lease, performs the work while publishing progress
// through the event bus it was constructed with, and releases the
// lease. RunMaintenance is the sanctioned entry point that orders
// Validate before Run and lives on AdminStore, so DataStore consumers
// cannot start an agent.
type MaintenanceAgent interface {
	// Validate checks whether the operation is applicable to the
	// current state of the Store: required maintenance mode, presence
	// of required parameters, availability of dependencies. It must
	// not perform irreversible work.
	Validate(ctx context.Context) error

	// Run starts the operation. It acquires a maintenance lease,
	// performs the work, and releases the lease. It returns the result
	// with accumulated statistics.
	Run(ctx context.Context) (*AgentResult, error)
}

// AgentResult is the result of an agent's work (one-shot or one
// background cycle). Used in EventAgentCompleted and EventAgentCycle.
type AgentResult struct {
	AgentType   string
	StoreID     string
	StartedAt   time.Time
	CompletedAt time.Time
	Stats       map[string]int64
	Partial     bool // true if the work was interrupted and completed only partially
}
