package wrapper_test

import (
	"testing"

	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper"
)

// fakeWrapper is a minimal Factory for registry round-trip tests. Wrap is
// never called by these tests; it exists only to satisfy the interface.
type fakeWrapper struct {
	name  string
	class wrapper.Class
}

func (f fakeWrapper) Wrap(inner store.DataStore, _ wrapper.Deps) (store.DataStore, error) {
	return inner, nil
}

func (f fakeWrapper) Descriptor() wrapper.Descriptor {
	return wrapper.Descriptor{Name: f.name, Class: f.class}
}

func TestRegister_LookupRoundTrip(t *testing.T) {
	wrapper.Register(fakeWrapper{name: "test.alpha", class: wrapper.Behavioral})
	got, ok := wrapper.Lookup("test.alpha")
	if !ok {
		t.Fatal(`Lookup("test.alpha") = false after Register`)
	}
	if d := got.Descriptor(); d.Name != "test.alpha" || d.Class != wrapper.Behavioral {
		t.Fatalf("Descriptor = %+v, want {test.alpha behavioral}", d)
	}
}

func TestLookup_Unknown(t *testing.T) {
	if _, ok := wrapper.Lookup("test.never-registered"); ok {
		t.Fatal("Lookup(unknown) = true")
	}
}

// TestRegister_RejectsBadInput checks Register panics on programmer errors
// (nil factory, empty name, duplicate). The duplicate case registers a
// unique name twice so the test is self-contained.
func TestRegister_RejectsBadInput(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: Register did not panic", name)
			}
		}()
		fn()
	}
	mustPanic("nil factory", func() { wrapper.Register(nil) })
	mustPanic("empty name", func() { wrapper.Register(fakeWrapper{name: "", class: wrapper.Behavioral}) })

	const dup = "test.dup-probe"
	wrapper.Register(fakeWrapper{name: dup, class: wrapper.Behavioral}) // first: ok
	mustPanic("duplicate", func() { wrapper.Register(fakeWrapper{name: dup, class: wrapper.Behavioral}) })
}

func TestClass_String(t *testing.T) {
	for c, want := range map[wrapper.Class]string{
		wrapper.Structural: "structural",
		wrapper.Behavioral: "behavioral",
		wrapper.Class(9):   "Class(9)",
	} {
		if got := c.String(); got != want {
			t.Errorf("Class(%d).String() = %q, want %q", uint8(c), got, want)
		}
	}
}
