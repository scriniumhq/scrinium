package dagcheck

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ModulePath is the project's root module path. Used to filter
// internal imports from stdlib and external dependencies.
const ModulePath = "github.com/rkurbatov/scrinium"

// packageInfo is the result of `go list -deps -json` for a single
// package. The fields match the JSON output of go list.
type packageInfo struct {
	ImportPath  string   `json:"ImportPath"`
	Imports     []string `json:"Imports"`
	TestImports []string `json:"TestImports"`
}

// ListProjectPackages returns a map from each internal package's
// import path to the list of other internal packages it imports.
// Stdlib and external modules are filtered out.
//
// TestImports are NOT taken into account: tests may import any
// project package (for example, integration_test in core imports
// event, which would be allowed regardless). The DAG is checked
// against production dependencies.
func ListProjectPackages() (map[string][]string, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", ModulePath+"/...")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list failed: %w", err)
	}

	// `go list -json ...` emits several JSON objects in sequence,
	// not an array.
	dec := json.NewDecoder(strings.NewReader(string(out)))
	result := make(map[string][]string)

	for dec.More() {
		var p packageInfo
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		// Only packages from the current module are interesting.
		if !strings.HasPrefix(p.ImportPath, ModulePath) {
			continue
		}
		// Filter imports — keep only the internal ones.
		var internal []string
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, ModulePath) {
				internal = append(internal, imp)
			}
		}
		sort.Strings(internal)
		result[p.ImportPath] = internal
	}

	return result, nil
}

// Layer is a logical layer of a package per the DAG. Used for fast
// rule checks.
type Layer int

const (
	LayerEvent Layer = iota
	LayerDriver
	LayerCore
	LayerPlugin
	LayerIndex
	LayerCurator
	LayerCuratorSub // bundler, chunker, host
	LayerAgent
	LayerMaintenance
	LayerProjection
	LayerCmd
	LayerInternal // dagcheck and other test infra
	LayerUnknown
)

// String returns a human-readable name for the layer.
func (l Layer) String() string {
	switch l {
	case LayerEvent:
		return "event"
	case LayerDriver:
		return "driver"
	case LayerCore:
		return "core"
	case LayerPlugin:
		return "plugin"
	case LayerIndex:
		return "index"
	case LayerCurator:
		return "curator"
	case LayerCuratorSub:
		return "curator/sub"
	case LayerAgent:
		return "agent"
	case LayerMaintenance:
		return "maintenance"
	case LayerProjection:
		return "projection"
	case LayerCmd:
		return "cmd"
	case LayerInternal:
		return "internal"
	default:
		return "unknown"
	}
}

// LayerOf identifies the layer of a package by its import path.
func LayerOf(pkg string) Layer {
	suffix := strings.TrimPrefix(pkg, ModulePath)
	suffix = strings.TrimPrefix(suffix, "/")
	if suffix == "" {
		return LayerUnknown
	}
	parts := strings.Split(suffix, "/")
	switch parts[0] {
	case "event":
		return LayerEvent
	case "driver":
		return LayerDriver
	case "core":
		return LayerCore
	case "plugin":
		return LayerPlugin
	case "index":
		return LayerIndex
	case "curator":
		if len(parts) == 1 {
			return LayerCurator
		}
		return LayerCuratorSub
	case "agent":
		return LayerAgent
	case "maintenance":
		return LayerMaintenance
	case "projection":
		return LayerProjection
	case "cmd":
		return LayerCmd
	case "internal":
		return LayerInternal
	}
	return LayerUnknown
}

// AllowedTargets returns the set of layers that the package at
// layer `from` is allowed to import. LayerInternal carries no
// restrictions (test infrastructure).
func AllowedTargets(from Layer) map[Layer]bool {
	switch from {
	case LayerEvent:
		// Leaf package: nothing from the project is importable.
		return map[Layer]bool{}

	case LayerDriver:
		return map[Layer]bool{LayerEvent: true}

	case LayerCore:
		return map[Layer]bool{LayerEvent: true, LayerDriver: true}

	case LayerPlugin:
		return map[Layer]bool{LayerEvent: true, LayerDriver: true, LayerCore: true}

	case LayerIndex:
		return map[Layer]bool{LayerEvent: true, LayerDriver: true, LayerCore: true}

	case LayerCurator:
		return map[Layer]bool{
			LayerEvent: true, LayerDriver: true, LayerCore: true,
			LayerCuratorSub: true,
			// curator/* subpackages may be imported from curator
			// itself when wiring (optional in M4).
		}

	case LayerCuratorSub:
		return map[Layer]bool{
			LayerEvent: true, LayerDriver: true, LayerCore: true,
			LayerCurator: true,
			// curator subpackages must not import each other (bundler
			// must not know about chunker and vice versa).
		}

	case LayerAgent:
		return map[Layer]bool{
			LayerEvent: true, LayerDriver: true, LayerCore: true,
		}

	case LayerMaintenance:
		// Maintenance may import curator for the TransitStore
		// contract (RebuildIndexAgent.HostStorage in RebuildConfig).
		return map[Layer]bool{
			LayerEvent: true, LayerDriver: true, LayerCore: true,
			LayerCurator: true,
		}

	case LayerProjection:
		// Projection does NOT import curator: the type assertion
		// to *curator.Curator is performed inside the
		// implementation, not in the contract.
		return map[Layer]bool{
			LayerEvent: true, LayerCore: true,
		}

	case LayerCmd:
		// Entry points may import anything from the upper layers.
		return map[Layer]bool{
			LayerEvent: true, LayerDriver: true, LayerCore: true,
			LayerPlugin: true, LayerIndex: true,
			LayerCurator: true, LayerCuratorSub: true,
			LayerAgent: true, LayerMaintenance: true, LayerProjection: true,
		}

	case LayerInternal:
		// Test infrastructure: everything is allowed.
		return map[Layer]bool{
			LayerEvent: true, LayerDriver: true, LayerCore: true,
			LayerPlugin: true, LayerIndex: true,
			LayerCurator: true, LayerCuratorSub: true,
			LayerAgent: true, LayerMaintenance: true, LayerProjection: true,
		}
	}
	return map[Layer]bool{}
}

// Violation is a single DAG breach.
type Violation struct {
	From      string // import path of the importing package
	To        string // import path of the imported one
	FromLayer Layer
	ToLayer   Layer
	Reason    string
}

func (v Violation) String() string {
	return fmt.Sprintf("[%s] %s ━━▶ [%s] %s: %s",
		v.FromLayer, v.From, v.ToLayer, v.To, v.Reason)
}

// CheckDAG verifies an imports map against the DAG rules. It
// returns the list of violations found.
func CheckDAG(imports map[string][]string) []Violation {
	var violations []Violation
	for pkg, deps := range imports {
		fromLayer := LayerOf(pkg)
		allowed := AllowedTargets(fromLayer)
		for _, dep := range deps {
			toLayer := LayerOf(dep)
			if toLayer == LayerUnknown {
				continue
			}
			// A self-import should not happen, but check explicitly.
			if pkg == dep {
				continue
			}
			// Subpackages within the same layer may import each
			// other (for example, curator/bundler ->
			// curator/host) — but that is governed by the
			// dedicated curator/sub rules.
			if !allowed[toLayer] {
				violations = append(violations, Violation{
					From: pkg, To: dep,
					FromLayer: fromLayer, ToLayer: toLayer,
					Reason: fmt.Sprintf("layer %q must not import layer %q",
						fromLayer, toLayer),
				})
			}
		}
	}
	// Stable order for reproducible test messages.
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].From != violations[j].From {
			return violations[i].From < violations[j].From
		}
		return violations[i].To < violations[j].To
	})
	return violations
}
