package buildinfo

import "testing"

// render is String()'s pure formatting, factored out so the test doesn't depend on
// the ambient build's VCS stamps.
func render(i Info) string {
	s := i.Tag
	if i.Commit != "" {
		s += " (" + i.Commit
		if i.Dirty {
			s += ", dirty"
		}
		s += ")"
	} else if i.Dirty {
		s += " (dirty)"
	}
	return s
}

func TestRender(t *testing.T) {
	cases := []struct {
		name string
		in   Info
		want string
	}{
		{"release clean", Info{Tag: "v0.1.0", Commit: "abc123abc123"}, "v0.1.0 (abc123abc123)"},
		{"dev clean", Info{Tag: "dev", Commit: "abc123abc123"}, "dev (abc123abc123)"},
		{"dev dirty", Info{Tag: "dev", Commit: "abc123abc123", Dirty: true}, "dev (abc123abc123, dirty)"},
		{"no commit", Info{Tag: "dev"}, "dev"},
		{"no commit dirty", Info{Tag: "dev", Dirty: true}, "dev (dirty)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := render(c.in); got != c.want {
				t.Errorf("render(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestStringMatchesRender guards that the exported String() stays in sync with the
// formatting the test pins (they must produce the same output for the live Info).
func TestStringMatchesRender(t *testing.T) {
	if String() != render(Get()) {
		t.Errorf("String() = %q, render(Get()) = %q — formatting drifted", String(), render(Get()))
	}
}
