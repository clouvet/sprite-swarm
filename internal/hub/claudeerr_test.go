package hub

import (
	"strings"
	"testing"
)

func TestClaudeErrorMessage(t *testing.T) {
	cases := []struct {
		name         string
		resultText   string
		subtype      string
		wantContains string
		wantIncident bool
	}{
		{"api overload text", "API Error: Overloaded", "error_during_execution", "Overloaded", true},
		{"529 status", "request failed with status 529", "", "529", true},
		{"plain error text, no incident hint", "the tool returned nothing useful", "error", "nothing useful", false},
		{"empty falls back to subtype", "", "error_max_turns", "error_max_turns", false},
		{"empty everything", "", "", "ended the turn with an error", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := claudeErrorMessage(c.resultText, c.subtype)
			if !strings.Contains(got, c.wantContains) {
				t.Errorf("message %q missing %q", got, c.wantContains)
			}
			hasIncident := strings.Contains(got, "status.claude.com")
			if hasIncident != c.wantIncident {
				t.Errorf("incident hint = %v, want %v (message: %q)", hasIncident, c.wantIncident, got)
			}
		})
	}
}
