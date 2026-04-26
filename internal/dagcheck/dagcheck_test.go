package dagcheck

import (
	"strings"
	"testing"
)

// TestDAG_RealProject is the main test: it runs `go list` on the
// current project and verifies that the import graph matches the
// DAG rules.
//
// This is insurance: if someone refactors and accidentally imports
// curator from core (or projection from agent), the test fails
// immediately, before the violation spreads through the codebase.
func TestDAG_RealProject(t *testing.T) {
	imports, err := ListProjectPackages()
	if err != nil {
		t.Fatalf("ListProjectPackages: %v", err)
	}
	if len(imports) == 0 {
		t.Fatal("no packages found; check that we're running from project root")
	}

	violations := CheckDAG(imports)
	if len(violations) > 0 {
		var msg strings.Builder
		msg.WriteString("DAG violations found:\n")
		for _, v := range violations {
			msg.WriteString("  ")
			msg.WriteString(v.String())
			msg.WriteString("\n")
		}
		t.Fatal(msg.String())
	}
}

// TestLayerOf is a unit test for the package classifier.
func TestLayerOf(t *testing.T) {
	cases := []struct {
		pkg  string
		want Layer
	}{
		{"github.com/rkurbatov/scrinium/event", LayerEvent},
		{"github.com/rkurbatov/scrinium/driver", LayerDriver},
		{"github.com/rkurbatov/scrinium/driver/localfs", LayerDriverSub},
		{"github.com/rkurbatov/scrinium/driver/faulty", LayerDriverSub},
		{"github.com/rkurbatov/scrinium/core", LayerCore},
		{"github.com/rkurbatov/scrinium/core/internal/descriptor", LayerCoreSub},
		{"github.com/rkurbatov/scrinium/plugin", LayerPlugin},
		{"github.com/rkurbatov/scrinium/plugin/compress/zstd", LayerPlugin},
		{"github.com/rkurbatov/scrinium/index", LayerIndex},
		{"github.com/rkurbatov/scrinium/index/sqlite", LayerIndexSub},
		{"github.com/rkurbatov/scrinium/curator", LayerCurator},
		{"github.com/rkurbatov/scrinium/curator/bundler", LayerCuratorSub},
		{"github.com/rkurbatov/scrinium/curator/chunker", LayerCuratorSub},
		{"github.com/rkurbatov/scrinium/curator/host", LayerCuratorSub},
		{"github.com/rkurbatov/scrinium/agent", LayerAgent},
		{"github.com/rkurbatov/scrinium/maintenance", LayerMaintenance},
		{"github.com/rkurbatov/scrinium/projection", LayerProjection},
		{"github.com/rkurbatov/scrinium/cmd/scrinium-fuse", LayerCmd},
		{"github.com/rkurbatov/scrinium/internal/dagcheck", LayerInternal},
	}
	for _, c := range cases {
		got := LayerOf(c.pkg)
		if got != c.want {
			t.Errorf("LayerOf(%q) = %s, want %s", c.pkg, got, c.want)
		}
	}
}

// TestCheckDAG_DetectsViolation is a negative test: feed the
// validator a mock graph with an obvious breach (core importing
// curator) and check that the validator catches it.
func TestCheckDAG_DetectsViolation(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/core": {
			"github.com/rkurbatov/scrinium/event",   // OK
			"github.com/rkurbatov/scrinium/driver",  // OK
			"github.com/rkurbatov/scrinium/curator", // VIOLATION
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	v := violations[0]
	if v.FromLayer != LayerCore || v.ToLayer != LayerCurator {
		t.Errorf("wrong violation: %s", v)
	}
}

// TestCheckDAG_AllowsCmdToImportEverything — cmd is allowed to
// import anything, including upper layers.
func TestCheckDAG_AllowsCmdToImportEverything(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/cmd/scrinium-fuse": {
			"github.com/rkurbatov/scrinium/projection",
			"github.com/rkurbatov/scrinium/curator",
			"github.com/rkurbatov/scrinium/agent",
			"github.com/rkurbatov/scrinium/maintenance",
			"github.com/rkurbatov/scrinium/core",
			"github.com/rkurbatov/scrinium/driver",
			"github.com/rkurbatov/scrinium/event",
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 0 {
		t.Errorf("expected no violations for cmd, got %d:\n%s",
			len(violations), formatViolations(violations))
	}
}

// TestCheckDAG_ProjectionMustNotImportCurator is the dedicated
// test for the inverted projection → curator dependency.
func TestCheckDAG_ProjectionMustNotImportCurator(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/projection": {
			"github.com/rkurbatov/scrinium/core",    // OK
			"github.com/rkurbatov/scrinium/event",   // OK
			"github.com/rkurbatov/scrinium/curator", // VIOLATION
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d:\n%s",
			len(violations), formatViolations(violations))
	}
	if violations[0].FromLayer != LayerProjection || violations[0].ToLayer != LayerCurator {
		t.Errorf("wrong violation: %s", violations[0])
	}
}

// TestCheckDAG_EventIsLeaf — event must not import anything from
// the project.
func TestCheckDAG_EventIsLeaf(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/event": {
			"github.com/rkurbatov/scrinium/core", // VIOLATION
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	if violations[0].FromLayer != LayerEvent {
		t.Errorf("wrong violation: %s", violations[0])
	}
}

// TestCheckDAG_AgentMustNotImportCurator — agents live at the
// same level as curator, but they must not import it.
func TestCheckDAG_AgentMustNotImportCurator(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/agent": {
			"github.com/rkurbatov/scrinium/core",
			"github.com/rkurbatov/scrinium/curator", // VIOLATION
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
}

// TestCheckDAG_DriverSubsMustNotImportEachOther — driver
// implementations live at the same level and must stay
// independent. faulty depending on localfs in production code
// would couple chaos tests to a specific backend.
func TestCheckDAG_DriverSubsMustNotImportEachOther(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/driver/faulty": {
			"github.com/rkurbatov/scrinium/driver",         // OK: contract
			"github.com/rkurbatov/scrinium/driver/localfs", // VIOLATION: sibling
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d:\n%s",
			len(violations), formatViolations(violations))
	}
	if violations[0].FromLayer != LayerDriverSub || violations[0].ToLayer != LayerDriverSub {
		t.Errorf("wrong violation: %s", violations[0])
	}
}

// TestCheckDAG_DriverSubMayImportContract — verifies the positive
// case: localfs and faulty must be allowed to import the driver
// contract.
func TestCheckDAG_DriverSubMayImportContract(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/driver/localfs": {
			"github.com/rkurbatov/scrinium/driver", // OK: parent contract
			"github.com/rkurbatov/scrinium/event",  // OK
		},
		"github.com/rkurbatov/scrinium/driver/faulty": {
			"github.com/rkurbatov/scrinium/driver", // OK: parent contract
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 0 {
		t.Errorf("expected no violations, got %d:\n%s",
			len(violations), formatViolations(violations))
	}
}

// TestCheckDAG_CoreSubMayImportFromCore — verifies that core may
// import its own internal helpers (descriptor and future
// internal/* packages). The reverse direction is the legitimate
// implementation pattern: core wires the helper, helper does the
// work.
func TestCheckDAG_CoreSubMayImportFromCore(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/core": {
			"github.com/rkurbatov/scrinium/core/internal/descriptor", // OK: own subpackage
			"github.com/rkurbatov/scrinium/driver",                   // OK
			"github.com/rkurbatov/scrinium/event",                    // OK
		},
		"github.com/rkurbatov/scrinium/core/internal/descriptor": {
			"github.com/rkurbatov/scrinium/driver", // OK: contract
			"github.com/rkurbatov/scrinium/event",  // OK
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 0 {
		t.Errorf("expected no violations, got %d:\n%s",
			len(violations), formatViolations(violations))
	}
}

// TestCheckDAG_IndexSubMayImportContract — verifies that index/sqlite
// (and future siblings) may import the index umbrella package and
// core. The umbrella may also accept imports from its subpackages.
func TestCheckDAG_IndexSubMayImportContract(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/index/sqlite": {
			"github.com/rkurbatov/scrinium/core",  // OK: StoreIndex contract
			"github.com/rkurbatov/scrinium/index", // OK: IndexOption surface
			"github.com/rkurbatov/scrinium/event", // OK
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 0 {
		t.Errorf("expected no violations, got %d:\n%s",
			len(violations), formatViolations(violations))
	}
}

// TestCheckDAG_IndexSubsMustNotImportEachOther — sqlite and postgres
// (when it lands) must stay independent. A future Postgres
// implementation cannot inherit from sqlite by accident.
func TestCheckDAG_IndexSubsMustNotImportEachOther(t *testing.T) {
	imports := map[string][]string{
		"github.com/rkurbatov/scrinium/index/postgres": {
			"github.com/rkurbatov/scrinium/index",        // OK: umbrella
			"github.com/rkurbatov/scrinium/index/sqlite", // VIOLATION: sibling
		},
	}
	violations := CheckDAG(imports)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d:\n%s",
			len(violations), formatViolations(violations))
	}
	if violations[0].FromLayer != LayerIndexSub || violations[0].ToLayer != LayerIndexSub {
		t.Errorf("wrong violation: %s", violations[0])
	}
}

func formatViolations(vs []Violation) string {
	var b strings.Builder
	for _, v := range vs {
		b.WriteString("  ")
		b.WriteString(v.String())
		b.WriteString("\n")
	}
	return b.String()
}
