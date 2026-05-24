package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"scrinium.dev/engine/domain"
)

// RunMaintenance is the AdminStore entry point for one-shot
// MaintenanceAgents (the contract lives in domain — see
// domain.MaintenanceAgent — because store names it here while agent
// and maintenance, which implement it, import store; domain is the
// leaf below that edge).
//
// It deliberately does not consult checkOperational: an agent may
// legitimately require the Store in MaintenanceModeOffline or ReadOnly,
// and Validate is where that precondition is enforced. The only states
// RunMaintenance itself refuses are a closed Store and a cancelled
// context.
//
// The agent owns the maintenance lease and emits its own progress and
// outcome events through the bus it was constructed with — RunMaintenance
// neither acquires the lease nor publishes agent.* events. It simply
// orders Validate before Run and surfaces the result.
func (s *store) RunMaintenance(ctx context.Context, agent domain.MaintenanceAgent) (*domain.AgentResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, fmt.Errorf("store.RunMaintenance: nil agent")
	}

	s.stateMu.RLock()
	closed := s.closed
	s.stateMu.RUnlock()
	if closed {
		return nil, os.ErrClosed
	}

	log := s.componentLogger("maintenance")
	if err := agent.Validate(ctx); err != nil {
		log.LogAttrs(ctx, slog.LevelDebug, "maintenance agent validation failed",
			storeIDAttr(s), slog.String("error", err.Error()))
		return nil, fmt.Errorf("store.RunMaintenance: validate: %w", err)
	}

	result, err := agent.Run(ctx)
	if err != nil {
		log.LogAttrs(ctx, slog.LevelDebug, "maintenance agent run failed",
			storeIDAttr(s), slog.String("error", err.Error()))
		return result, fmt.Errorf("store.RunMaintenance: run: %w", err)
	}
	log.LogAttrs(ctx, slog.LevelDebug, "maintenance agent completed", storeIDAttr(s))
	return result, nil
}
