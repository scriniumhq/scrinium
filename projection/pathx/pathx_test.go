package pathx

import "testing"

func TestLastSegment(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"", ""},
		{"foo", "foo"},
		{"foo/bar", "bar"},
		{"foo/bar/baz", "baz"},
		{"/foo", "foo"},
		{"/foo/bar", "bar"},
		{"foo/", ""},
		{"foo/bar/", ""},
		{"/", ""},
		{"//", ""},
		{"/foo//bar", "bar"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := LastSegment(tc.in); got != tc.want {
				t.Errorf("LastSegment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParent(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"", ""},
		{"foo", ""},
		{"foo/bar", "foo"},
		{"foo/bar/baz", "foo/bar"},
		{"/foo", ""},
		{"/foo/bar", "/foo"},
		{"foo/", "foo"},
		{"/", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := Parent(tc.in); got != tc.want {
				t.Errorf("Parent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSplitFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, wantFirst, wantRest string
	}{
		{"", "", ""},
		{"a", "a", ""},
		{"a/b", "a", "b"},
		{"a/b/c", "a", "b/c"},
		{"/a", "", "a"},
		{"a/", "a", ""},
		{"/", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			f, r := SplitFirst(tc.in)
			if f != tc.wantFirst || r != tc.wantRest {
				t.Errorf("SplitFirst(%q) = (%q, %q), want (%q, %q)",
					tc.in, f, r, tc.wantFirst, tc.wantRest)
			}
		})
	}
}

func TestJoin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		parent, child, want string
	}{
		{"", "", ""},
		{"a", "", "a"},
		{"", "b", "b"},
		{"a", "b", "a/b"},
		{"a/b", "c", "a/b/c"},
		{"a/", "b", "a//b"}, // does NOT normalise
		{"a", "/b", "a//b"}, // does NOT normalise
	}
	for _, tc := range tests {
		t.Run(tc.parent+"|"+tc.child, func(t *testing.T) {
			t.Parallel()
			if got := Join(tc.parent, tc.child); got != tc.want {
				t.Errorf("Join(%q, %q) = %q, want %q",
					tc.parent, tc.child, got, tc.want)
			}
		})
	}
}

func TestIsUnder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		p, prefix string
		want      bool
	}{
		{"", "", false},
		{"a", "", true},
		{"a/b", "", true},
		{"a/b/c", "a/b", true},
		{"a/b", "a/b", true},
		{"a/bc", "a/b", false},
		{"a", "a", true},
		{"a", "b", false},
	}
	for _, tc := range tests {
		t.Run(tc.p+"|"+tc.prefix, func(t *testing.T) {
			t.Parallel()
			if got := IsUnder(tc.p, tc.prefix); got != tc.want {
				t.Errorf("IsUnder(%q, %q) = %v, want %v",
					tc.p, tc.prefix, got, tc.want)
			}
		})
	}
}

func TestIsStrictUnder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		p, prefix string
		want      bool
	}{
		{"", "", false},
		{"a", "", true},
		{"a/b/c", "a/b", true},
		{"a/b", "a/b", false},
		{"a/bc", "a/b", false},
		{"a", "a", false},
	}
	for _, tc := range tests {
		t.Run(tc.p+"|"+tc.prefix, func(t *testing.T) {
			t.Parallel()
			if got := IsStrictUnder(tc.p, tc.prefix); got != tc.want {
				t.Errorf("IsStrictUnder(%q, %q) = %v, want %v",
					tc.p, tc.prefix, got, tc.want)
			}
		})
	}
}
