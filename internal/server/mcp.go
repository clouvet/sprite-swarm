package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// serveMCP lists (GET) or adds/updates (POST) user-added MCP servers in the brain
// registry, mirroring /api/env. GET returns {name: config}. POST body is
// {"name":"<name>","config":{...}} where config is a standard MCP server entry
// ({"command","args","env"} for stdio, or a url-based remote server). A change is
// applied by regenerating mcp.json + restarting active sessions on THIS sprite;
// other sprites pick it up on next boot (the registry is fleet-wide in the brain).
func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		reg, err := s.fleet.MCPRegistry(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, reg)
	case http.MethodPost:
		var body struct {
			Name   string          `json:"name"`
			Config json.RawMessage `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" || len(body.Config) == 0 {
			http.Error(w, `name and config are required`, http.StatusBadRequest)
			return
		}
		if err := s.fleet.SetMCPServer(r.Context(), body.Name, body.Config); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.applyMCP(); err != nil {
			http.Error(w, "saved, but apply failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]string{"added": body.Name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveMCPByName removes a server: DELETE /api/mcp/<name>.
func (s *Server) serveMCPByName(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/mcp/")
	if r.Method != http.MethodDelete || name == "" {
		http.Error(w, "DELETE /api/mcp/<name>", http.StatusMethodNotAllowed)
		return
	}
	if err := s.fleet.DeleteMCPServer(r.Context(), name); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.applyMCP(); err != nil {
		http.Error(w, "removed, but apply failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"removed": name})
}

// serveMCPRefresh recomposes mcp.json and restarts this sprite's active sessions
// WITHOUT changing the registry: POST /api/mcp/refresh. It re-runs the built-in
// connector discovery (Slack, Grafana, …), so a server that was dropped at boot
// because of a transient gateway hiccup reconnects — a self-heal that avoids a
// full agent reexec. Registered as an exact path so it wins over "/api/mcp/".
func (s *Server) serveMCPRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST /api/mcp/refresh", http.StatusMethodNotAllowed)
		return
	}
	if err := s.applyMCP(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"refreshed": "ok"})
}

// applyMCP recomposes mcp.json (built-ins + registry) and restarts this sprite's
// active sessions so the new server set takes effect (Claude won't hot-reload
// --mcp-config). No-op if the regenerator isn't wired.
func (s *Server) applyMCP() error {
	if s.regenerateMCP == nil {
		return nil
	}
	path, err := s.regenerateMCP()
	if err != nil {
		return err
	}
	s.hub.SetMCPConfigPath(path)
	s.hub.RestartActiveSessions()
	return nil
}
