package server

import "testing"

func TestLooksLikeTitle(t *testing.T) {
	good := []string{
		"Zed with remote dev environment",
		"GitHub action queue resolved",
		"Goal",
		"Debugging a stuck CI job",
	}
	for _, s := range good {
		if !looksLikeTitle(s) {
			t.Errorf("expected %q to be accepted as a title", s)
		}
	}
	// The real-world garbage: the model answered/continued instead of titling.
	bad := []string{
		"",
		"I'd need more context to debug this. Can you tell me:",
		"I printed it above. Is there something else you need?",
		"Sure! Here is a summary of what we discussed in this long conversation so far",
	}
	for _, s := range bad {
		if looksLikeTitle(s) {
			t.Errorf("expected %q to be rejected (conversational, not a title)", s)
		}
	}
}
