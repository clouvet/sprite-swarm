package fleet

import "os"

// envRole returns the role this agent should advertise (home | worker), from
// SPRITE_AGENT_ROLE. Empty means "worker" (the default for a spawned agent).
// "home" is set on the durable, pinned agent (DESIGN §10 "Home base").
func envRole() string { return os.Getenv("SPRITE_AGENT_ROLE") }
