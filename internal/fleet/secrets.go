package fleet

import (
	"context"
	"path"
	"strings"
)

// Operational secrets live in the brain so every sprite rehydrates the same
// capabilities (DESIGN §2.1 symmetry, §4.2 "everything else rehydrates"): the
// Sprites API token (spawn/reap) and the GitHub token (git/gh). The only
// out-of-brain input is how to reach the brain; capabilities come from it, so any
// sprite — home or a fresh worker — is equally capable.
//
// They sit under fleet/config/secrets/, part of the control-plane prefix
// (fleet/config/*) that agents only READ (the can-modify-policy guardrail, §6.2).
// Brain access == fleet-wide trust by design, so guarding the brain + scoping the
// stored tokens is what bounds blast radius.
const (
	SecretSpritesAPIToken  = "sprites-api-token"
	SecretGitHubToken      = "github"
	SecretFlyToken         = "fly"
	SecretClaudeOAuthToken = "claude-oauth-token" // Claude subscription (from `claude setup-token`)
)

func secretKey(name string) string { return path.Join("fleet", "config", "secrets", name) }

// GetSecret returns a named operational secret from the brain, or "" if unset.
func (s *Service) GetSecret(ctx context.Context, name string) string {
	data, err := s.brain.Get(ctx, secretKey(name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// PutSecret stores a named operational secret (used to seed the brain).
func (s *Service) PutSecret(ctx context.Context, name, value string) error {
	return s.brain.Put(ctx, secretKey(name), []byte(strings.TrimSpace(value)))
}
