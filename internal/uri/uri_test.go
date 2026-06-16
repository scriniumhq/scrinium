package uri

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestScheme(t *testing.T) {
	cases := map[string]string{
		"file:///abs":      "file",
		"sqlite:///x/i.db": "sqlite",
		"s3://bucket/key":  "s3",
		"/abs/path":        "",
		"./rel":            "",
		"~/home/rel":       "",
		"rel/path":         "",
	}
	for in, want := range cases {
		if got := Scheme(in); got != want {
			t.Errorf("Scheme(%q) = %q, want %q", in, got, want)
		}
		if got := IsURI(in); got != (want != "") {
			t.Errorf("IsURI(%q) = %v, want %v", in, got, want != "")
		}
	}
}

func TestResolveLocalPath_Absolute(t *testing.T) {
	u, _ := url.Parse("file:///abs/path")
	got, err := ResolveLocalPath(u)
	if err != nil {
		t.Fatalf("ResolveLocalPath: %v", err)
	}
	if got != "/abs/path" {
		t.Errorf("got %q, want /abs/path", got)
	}
}

func TestResolveLocalPath_TildeHostExpands(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	u, _ := url.Parse("file://~/relative")
	got, err := ResolveLocalPath(u)
	if err != nil {
		t.Fatalf("ResolveLocalPath: %v", err)
	}
	if want := filepath.Join(home, "relative"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveLocalPath_DotHostExpandsToCwd(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Skipf("no cwd: %v", err)
	}
	u, _ := url.Parse("file://./relative")
	got, err := ResolveLocalPath(u)
	if err != nil {
		t.Fatalf("ResolveLocalPath: %v", err)
	}
	if want := filepath.Join(cwd, "relative"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveLocalPath_ForeignHostRejected(t *testing.T) {
	u, _ := url.Parse("file://example.com/path")
	if _, err := ResolveLocalPath(u); !errors.Is(err, ErrUnsupportedHost) {
		t.Errorf("want ErrUnsupportedHost, got %v", err)
	}
}

func TestResolveLocalPath_EmptyPath(t *testing.T) {
	u, _ := url.Parse("file://")
	if _, err := ResolveLocalPath(u); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("want ErrEmptyPath, got %v", err)
	}
}

func TestResolveLocalURI_BareTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	got, err := ResolveLocalURI("~/store")
	if err != nil {
		t.Fatalf("ResolveLocalURI: %v", err)
	}
	if want := filepath.Join(home, "store"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveLocalURI_BareRelativeIsAbs(t *testing.T) {
	cwd, _ := os.Getwd()
	got, err := ResolveLocalURI("./data")
	if err != nil {
		t.Fatalf("ResolveLocalURI: %v", err)
	}
	if want := filepath.Join(cwd, "data"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveLocalURI_FileTildeMatchesBare(t *testing.T) {
	a, err1 := ResolveLocalURI("file://~/store")
	b, err2 := ResolveLocalURI("~/store")
	if err1 != nil || err2 != nil {
		t.Fatalf("errs: %v / %v", err1, err2)
	}
	if a != b {
		t.Errorf("file://~/store (%q) != ~/store (%q)", a, b)
	}
}

func TestResolveLocalURI_NonLocalScheme(t *testing.T) {
	if _, err := ResolveLocalURI("s3://bucket/key"); !errors.Is(err, ErrNotLocal) {
		t.Errorf("want ErrNotLocal, got %v", err)
	}
}
