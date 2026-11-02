package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// operatorKey is the brain config value holding the operator-identity blob: who
	// the human running this fleet is and the handles they go by across platforms.
	// Fleet-wide (set once, every sprite injects the same identity) and EMPTY by
	// default — a fresh clone of the repo ships no personal data; the identity is
	// configuration that lives only in this fleet's brain, never in source. Not a
	// secret as such, but it's personal, so it belongs here beside the other
	// fleet/config values rather than compiled into the binary.
	operatorKey = "fleet/config/operator"
	operatorTTL = 60 * time.Second // cache so the per-turn context hook stays fast
)

// OperatorIdentity returns the configured operator-identity blob, or "" when none
// is set. Cached ~60s so the per-turn context hook never waits on the brain.
func (s *Service) OperatorIdentity(ctx context.Context) string {
	s.mu.Lock()
	if !s.opFetched.IsZero() && s.now().Sub(s.opFetched) < operatorTTL {
		v := s.opIdentity
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()

	v := ""
	if b, err := s.brain.Get(ctx, operatorKey); err == nil {
		v = strings.TrimSpace(string(b))
	}
	s.mu.Lock()
	s.opIdentity, s.opFetched = v, s.now()
	s.mu.Unlock()
	return v
}

// SetOperatorIdentity persists the operator-identity blob fleet-wide (brain). An
// empty value clears it (the context block then disappears).
func (s *Service) SetOperatorIdentity(ctx context.Context, identity string) error {
	identity = strings.TrimSpace(identity)
	if err := s.brain.Put(ctx, operatorKey, []byte(identity)); err != nil {
		return err
	}
	s.mu.Lock()
	s.opIdentity, s.opFetched = identity, s.now()
	s.mu.Unlock()
	return nil
}

// operatorContext renders the operator-identity block injected each turn, right
// after the '## Now' line. It carries the generic attribution rule (this is the
// person in the chat, the same across platforms, refer to them in the second
// person) with the configured identity blob interpolated. Empty when unset, so no
// personal data ever appears unless this fleet configured it.
func (s *Service) operatorContext(ctx context.Context) string {
	id := s.OperatorIdentity(ctx)
	if id == "" {
		return ""
	}
	return fmt.Sprintf("## Operator\n"+
		"The human you work for — the person in this chat who dispatches your tasks — is %s. "+
		"That is ONE person, the same across every platform. When you meet any of these identities while "+
		"reading Slack, GitHub, Discourse, or email, that is THIS user — your operator — NOT a third party. "+
		"So a message, PR, commit, or email under those handles is something the person in front of you did: "+
		"refer to them as \"you\"/\"your\", and never narrate their own actions back to them in the third "+
		"person as if a stranger did them (not \"<their name> said in #channel…\" but \"you said in "+
		"#channel…\"). Name a third party only when it genuinely is someone else.\n", id)
}
