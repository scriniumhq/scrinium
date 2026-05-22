package store

import (
	"context"
	"fmt"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/reconcile"
)

// healReplicas applies Reconcile's repair action: writes the
// damaged or missing replica from the canonical descriptor.
// HealNone is a no-op; the four healing actions reduce to two
// distinct disk operations (write L0 only, write L1 only) since
// the canonical content already lives on the surviving side.
func healReplicas(ctx context.Context, drv driver.Driver, canonical *descriptor.Descriptor, action reconcile.Action) error {
	switch action {
	case reconcile.HealNone:
		return nil
	case reconcile.HealL0FromL1, reconcile.HealBothFromL1:
		// HealL0FromL1: L0 was missing/corrupted, rewrite it.
		// HealBothFromL1: sequence-divergence, L1 won, rewrite L0.
		// Same disk operation; distinct names preserve diagnostic
		// detail in logs.
		return descriptor.WriteReplica(ctx, drv, canonical, descriptor.L0)
	case reconcile.HealL1FromL0, reconcile.HealBothFromL0:
		return descriptor.WriteReplica(ctx, drv, canonical, descriptor.L1)
	default:
		return fmt.Errorf("core: unknown ReconcileAction %d", int(action))
	}
}
