package main

import (
	"strings"
	"testing"

	"github.com/clouvet/sprite-agent/internal/config"
)

// The affordance must tell the truth about GitHub: claim access only when a token
// is wired, and warn otherwise (so a no-PAT fleet's agent doesn't try git/gh).
func TestFleetAffordanceGitHubHonesty(t *testing.T) {
	cfg := config.Config{AgentID: "x"}
	withGH := fleetAffordance(cfg, false, true)
	if !strings.Contains(withGH, "You have GitHub access") {
		t.Fatalf("expected GitHub-access claim when token present")
	}
	noGH := fleetAffordance(cfg, false, false)
	if strings.Contains(noGH, "You have GitHub access") || !strings.Contains(noGH, "NO GitHub access") {
		t.Fatalf("expected NO-GitHub warning when token absent, got:\n%s", noGH)
	}
}
