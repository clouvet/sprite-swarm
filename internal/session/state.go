package session

import "log"

// StateTransition is an edge in the session state machine.
type StateTransition struct {
	From State
	To   State
}

// String returns a human-readable state name.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "IDLE"
	case StateWebOnly:
		return "WEB_ONLY"
	case StateTerminalOnly:
		return "TERMINAL_ONLY"
	case StateTransitioning:
		return "TRANSITIONING"
	default:
		return "UNKNOWN"
	}
}

// TransitionTo moves to newState if the edge is valid; otherwise logs and no-ops.
func (s *Session) TransitionTo(newState State) {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldState := s.State
	if !isValidTransition(oldState, newState) {
		log.Printf("[%s] invalid state transition: %s -> %s", s.ID, oldState, newState)
		return
	}
	s.State = newState
	log.Printf("[%s] state transition: %s -> %s", s.ID, oldState, newState)
}

func isValidTransition(from, to State) bool {
	valid := map[StateTransition]bool{
		{StateIdle, StateWebOnly}:               true,
		{StateWebOnly, StateTransitioning}:      true,
		{StateWebOnly, StateTerminalOnly}:       true,
		{StateTransitioning, StateTerminalOnly}: true,
		{StateTerminalOnly, StateWebOnly}:       true,
		{StateWebOnly, StateIdle}:               true,
		{StateTerminalOnly, StateIdle}:          true,
	}
	return valid[StateTransition{from, to}]
}
