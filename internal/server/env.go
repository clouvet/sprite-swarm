package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// envNameRe is a conventional environment variable name.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// serveEnv manages the worker's in-memory env secrets.
//
//	GET  /api/env         → {"names": [...]}  (names only — values are never returned)
//	POST /api/env {name,value} → upsert (overwrite allowed); returns {"names": [...]}
func (s *Server) serveEnv(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string][]string{"names": s.secrets.Names()})
	case http.MethodPost:
		var body struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(body.Name)
		if !envNameRe.MatchString(name) {
			http.Error(w, "invalid variable name (use letters, digits, underscore; not starting with a digit)", http.StatusBadRequest)
			return
		}
		if body.Value == "" {
			http.Error(w, "value required", http.StatusBadRequest)
			return
		}
		s.secrets.Set(name, body.Value) // upsert — overwriting is allowed
		writeJSON(w, map[string][]string{"names": s.secrets.Names()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveEnvByName handles DELETE /api/env/{name}.
func (s *Server) serveEnvByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/env/")
	if name == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.secrets.Delete(name)
	w.WriteHeader(http.StatusNoContent)
}
