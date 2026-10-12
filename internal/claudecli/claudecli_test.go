package claudecli

import "testing"

// The CLI's `--version` output reads like "2.1.281 (Claude Code)"; we only want the
// semver. Guard the extraction against the shapes we've seen so a format tweak that
// breaks it is caught here, not by a silently-empty version in production.
func TestVersionExtraction(t *testing.T) {
	cases := map[string]string{
		"2.1.281 (Claude Code)":          "2.1.281",
		"2.1.281":                        "2.1.281",
		"claude 2.10.3 (Claude Code)\n":  "2.10.3",
		"":                               "",
		"unknown":                        "",
		"Claude Code version 2.0.0-beta": "2.0.0",
	}
	for in, want := range cases {
		if got := verRe.FindString(in); got != want {
			t.Errorf("verRe.FindString(%q) = %q, want %q", in, got, want)
		}
	}
}
