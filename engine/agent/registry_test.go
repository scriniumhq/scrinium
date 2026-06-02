package agent_test

import (
	"context"
	"errors"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
)

// fakeAgent is a minimal Agent for registry round-trip tests.
type fakeAgent struct{ kind string }

func (a fakeAgent) AgentType() string              { return a.kind }
func (a fakeAgent) Validate(context.Context) error { return nil }
func (a fakeAgent) Status() (agent.State, error)   { return agent.StateIdle, nil }
func (a fakeAgent) Run(context.Context) (*domain.AgentResult, error) {
	return &domain.AgentResult{AgentType: a.kind}, nil
}

type fakeFactory struct{ kind string }

func (f fakeFactory) Name() string { return f.kind }
func (f fakeFactory) Build(_ store.Store, _ any, _ agent.AgentDeps) (agent.Agent, error) {
	return fakeAgent{kind: f.kind}, nil
}

// TestRegistry_BuiltinsRegistered checks the built-in agents register
// themselves through their init().
func TestRegistry_BuiltinsRegistered(t *testing.T) {
	for _, kind := range []string{"gc", "scrub", "snapshot", "rebuild"} {
		if _, ok := agent.Lookup(kind); !ok {
			t.Errorf("Lookup(%q) = false, want a registered built-in factory", kind)
		}
	}
}

// TestRegistry_RegisterLookupBuild exercises a full round trip with a
// custom, namespaced agent type.
func TestRegistry_RegisterLookupBuild(t *testing.T) {
	const kind = "acme.registry-probe"
	agent.Register(fakeFactory{kind: kind})

	if _, ok := agent.Lookup(kind); !ok {
		t.Fatalf("Lookup(%q) = false after Register", kind)
	}
	a, err := agent.Build(kind, nil, nil, agent.AgentDeps{})
	if err != nil {
		t.Fatalf("Build(%q) error: %v", kind, err)
	}
	if a == nil || a.AgentType() != kind {
		t.Fatalf("Build(%q) returned %v, want AgentType %q", kind, a, kind)
	}
}

// TestRegistry_InvalidAgentType checks that an ill-formed kind is
// rejected with ErrInvalidAgentType rather than panicking.
func TestRegistry_InvalidAgentType(t *testing.T) {
	for _, kind := range []string{"GC", "bad name", "Has_Underscore", ""} {
		if _, err := agent.Build(kind, nil, nil, agent.AgentDeps{}); !errors.Is(err, errs.ErrInvalidAgentType) {
			t.Errorf("Build(%q) error = %v, want ErrInvalidAgentType", kind, err)
		}
	}
}

// TestRegistry_UnknownKind checks that a well-formed but unregistered
// kind also yields ErrInvalidAgentType.
func TestRegistry_UnknownKind(t *testing.T) {
	if _, err := agent.Build("definitely-not-registered", nil, nil, agent.AgentDeps{}); !errors.Is(err, errs.ErrInvalidAgentType) {
		t.Errorf("Build(unknown) error = %v, want ErrInvalidAgentType", err)
	}
}

// TestRegistry_RegisterRejectsBadInput checks that Register panics on
// programmer errors (nil factory, ill-formed name, duplicate).
func TestRegistry_RegisterRejectsBadInput(t *testing.T) {
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: Register did not panic", name)
			}
		}()
		f()
	}
	mustPanic("nil factory", func() { agent.Register(nil) })
	mustPanic("invalid name", func() { agent.Register(fakeFactory{kind: "BadName"}) })
	mustPanic("duplicate", func() { agent.Register(fakeFactory{kind: "gc"}) })
}
