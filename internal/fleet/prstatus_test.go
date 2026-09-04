package fleet

import (
	"strings"
	"testing"
)

func TestFormatPRLine(t *testing.T) {
	cases := []struct {
		name          string
		branch        string
		pr            prInfo
		wantSubstr    string
		wantMergedFmt bool // expects "MERGED, not open" language
		wantEmpty     bool
	}{
		{
			name:       "open",
			branch:     "feat/x",
			pr:         prInfo{Number: 12, Title: "Add x", State: "OPEN"},
			wantSubstr: `PR #12 "Add x" is OPEN`,
		},
		{
			name:          "merged shows date + landed language",
			branch:        "feat/y",
			pr:            prInfo{Number: 34, Title: "Add y", State: "MERGED", MergedAt: "2026-09-04T10:11:12Z"},
			wantSubstr:    "on 2026-09-04",
			wantMergedFmt: true,
		},
		{
			name:       "closed unmerged",
			branch:     "feat/z",
			pr:         prInfo{Number: 56, Title: "Add z", State: "CLOSED"},
			wantSubstr: "CLOSED without merging",
		},
		{
			name:      "no pr (number 0) => empty",
			branch:    "main",
			pr:        prInfo{Number: 0, State: "OPEN"},
			wantEmpty: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatPRLine(c.branch, c.pr)
			if c.wantEmpty {
				if got != "" {
					t.Fatalf("want empty, got %q", got)
				}
				return
			}
			if !strings.Contains(got, c.wantSubstr) {
				t.Errorf("line %q missing %q", got, c.wantSubstr)
			}
			if c.wantMergedFmt && !strings.Contains(got, "MERGED, not open") {
				t.Errorf("merged line lacks landed language: %q", got)
			}
		})
	}
}

func TestFormatPRLineTruncatesTitle(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := formatPRLine("b", prInfo{Number: 1, Title: long, State: "OPEN"})
	if !strings.Contains(got, "…") {
		t.Errorf("expected long title truncated with …, got %q", got)
	}
}
