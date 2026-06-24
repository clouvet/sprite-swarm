package config

import (
	"path/filepath"
	"testing"
)

// TestSlugifyCwd locks the transcript-directory naming to match Claude Code's:
// every non-[a-zA-Z0-9-] rune becomes a dash. A regression here points history
// replay at the wrong directory, silently losing chat history on refresh.
func TestSlugifyCwd(t *testing.T) {
	cases := map[string]string{
		"/home/sprite":          "-home-sprite",
		"/home/sprite/.sa-home": "-home-sprite--sa-home", // the "." and leading "/" both -> "-"
		"/home/sprite/work_dir": "-home-sprite-work-dir",
		"/a/b.c/d":              "-a-b-c-d",
	}
	for in, want := range cases {
		if got := slugifyCwd(in); got != want {
			t.Errorf("slugifyCwd(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveProjectsDir(t *testing.T) {
	t.Setenv("HOME", "/home/sprite")
	got := deriveProjectsDir("/home/sprite/.sa-home")
	want := filepath.Join("/home/sprite", ".claude", "projects", "-home-sprite--sa-home")
	if got != want {
		t.Errorf("deriveProjectsDir = %q, want %q", got, want)
	}
}
