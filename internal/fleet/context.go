package fleet

import (
	"context"
	"fmt"
	"strings"
)

// FleetContext renders live fleet state for injection into the agent's context
// each turn (DESIGN §5 "inject live fleet state" + §2.4 presence-routing): the
// roster with status/liveness/presence, plus the shared-memory index. A
// UserPromptSubmit hook curls this so every turn the agent knows who exists,
// what they're doing, where the human is, and what durable memory exists.
//
// Presence-routing rule, made explicit to the agent: do NOT narrate or act on a
// worker a human is currently attached to — the human is steering it (§2.4).
func (s *Service) FleetContext(ctx context.Context, memLimit int) (string, error) {
	roster, err := s.roster(ctx)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Fleet (live) — you are %q\n", s.id)
	var attended []string
	for _, e := range roster {
		dot := "○" // not alive
		if e.Alive {
			dot = "●"
		}
		self := ""
		if e.ID == s.id {
			self = " (you)"
		}
		line := fmt.Sprintf("- %s%s · %s · %s · %q", e.ID, self, e.Role, dot, e.Phase)
		if e.Present && e.ID != s.id {
			line += "  👤 human attached → DEFER (don't act/narrate)"
			attended = append(attended, e.ID)
		}
		if e.Reapable {
			line += "  [reapable]"
		}
		b.WriteString(line + "\n")
	}
	if len(attended) > 0 {
		fmt.Fprintf(&b, "A human is steering: %s — defer to them on those workers.\n", strings.Join(attended, ", "))
	}

	mem, err := s.MemoryContext(ctx, memLimit)
	if err == nil && mem != "" {
		b.WriteString("\n")
		b.WriteString(mem)
	}
	return b.String(), nil
}
