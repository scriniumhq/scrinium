package event

import (
	"strings"
	"testing"
)

// allEventTypes is every reserved event-type string declared in this
// package, grouped by namespace. The test below asserts the flat-package
// invariant (C-1): all four namespaces live in one package and their
// prefixes are mutually exclusive.
var allEventTypes = map[string][]string{
	"store.": {
		EventManifestSaved,
		EventArtifactDeleted,
		EventBlobPhysicallyDeleted,
		EventRollbackCompleted,
		EventScrubFailed,
		EventStoreDegraded,
		EventOrphanScanCompleted,
		EventCapacityWarning,
		EventKEKRotated,
		EventConfigUpdated,
		EventStaleLeaseTakeover,
		EventPackSealed,
		EventPackCompacted,
		EventArtifactMigrated,
	},
	"agent.": {
		EventAgentStarted,
		EventAgentProgress,
		EventAgentCycle,
		EventAgentCompleted,
		EventAgentFailed,
		EventAgentStopped,
		EventAgentCancelled,
		EventAgentStaleLease,
	},
	"index.": {
		EventIndexWriteLatency,
		EventIndexContentionError,
		EventIndexSize,
	},
	"projection.": {
		EventPathCollision,
		EventViewRebuilt,
	},
}

// TestReservedNamespaces_Coverage asserts every reserved namespace is
// present in this single package and that each constant carries its
// namespace prefix.
func TestReservedNamespaces_Coverage(t *testing.T) {
	wantPrefixes := []string{"store.", "agent.", "index.", "projection."}
	for _, p := range wantPrefixes {
		types, ok := allEventTypes[p]
		if !ok || len(types) == 0 {
			t.Errorf("namespace %q: no event types declared in package event", p)
			continue
		}
		for _, typ := range types {
			if !strings.HasPrefix(typ, p) {
				t.Errorf("event type %q is grouped under %q but lacks the prefix", typ, p)
			}
		}
	}
}

// TestReservedNamespaces_PrefixesUnique asserts no event type matches
// more than one reserved prefix (the prefixes do not overlap) and that
// every type string is unique across the whole package.
func TestReservedNamespaces_PrefixesUnique(t *testing.T) {
	prefixes := []string{"store.", "agent.", "index.", "projection."}

	seen := map[string]string{} // type -> namespace it was found under
	for ns, types := range allEventTypes {
		for _, typ := range types {
			if prev, dup := seen[typ]; dup {
				t.Errorf("event type %q declared under both %q and %q", typ, prev, ns)
			}
			seen[typ] = ns

			matches := 0
			for _, p := range prefixes {
				if strings.HasPrefix(typ, p) {
					matches++
				}
			}
			if matches != 1 {
				t.Errorf("event type %q matches %d reserved prefixes, want exactly 1", typ, matches)
			}
		}
	}
}
