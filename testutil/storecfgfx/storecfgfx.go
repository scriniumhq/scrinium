// Package storecfgfx supplies ready-made config.StoreConfig values for
// tests.
//
// Historically it hand-duplicated the plain-store defaults because the
// defaults lived in a store-internal package testutil could not import.
// The configuration model is public now (scrinium.dev/config), so the
// fixture delegates: one source of truth, no drift.
package storecfgfx

import (
	"scrinium.dev/config"
)

// Plain returns the configuration of a default plain (unencrypted)
// store — exactly what config.ApplyDefaults yields for an empty input.
func Plain() config.StoreConfig {
	return config.ApplyDefaults(config.StoreConfig{})
}
