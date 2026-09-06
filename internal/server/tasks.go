package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/clouvet/sprite-swarm/internal/routines"
)

// serveTasks handles /api/tasks: GET lists this sprite's routines, POST creates a
// custom one. The agent is told about these endpoints in the fleet affordance prompt so
// the human can manage routines from chat ("schedule a task to…", "list my routines").
func (s *Server) serveTasks(w http.ResponseWriter, r *http.Request) {
	if s.routines == nil {
		http.Error(w, "routines not enabled", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"tasks": s.routines.Store().List()})
	case http.MethodPost:
		var body struct {
			Name        string `json:"name"`
			Prompt      string `json:"prompt"`
			IntervalMin int    `json:"interval_min"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		t, err := s.routines.Store().Create(body.Name, body.Prompt, body.IntervalMin)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, t)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveTaskByID handles /api/tasks/<id> and /api/tasks/<id>/run:
//   - GET    <id>       one task
//   - PATCH  <id>       {enabled?, interval_min?, prompt?, name?}
//   - DELETE <id>       delete a custom task (default tasks 409)
//   - POST   <id>/run   run the task now, in the background
func (s *Server) serveTaskByID(w http.ResponseWriter, r *http.Request) {
	if s.routines == nil {
		http.Error(w, "routines not enabled", http.StatusServiceUnavailable)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		http.Error(w, "task id required", http.StatusBadRequest)
		return
	}

	if action == "run" && r.Method == http.MethodPost {
		if err := s.routines.RunNow(context.Background(), id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"status": "running", "id": id})
		return
	}

	switch r.Method {
	case http.MethodGet:
		t, ok := s.routines.Store().Get(id)
		if !ok {
			http.Error(w, "no such task", http.StatusNotFound)
			return
		}
		writeJSON(w, t)
	case http.MethodPatch:
		var body struct {
			Name        *string `json:"name"`
			Prompt      *string `json:"prompt"`
			IntervalMin *int    `json:"interval_min"`
			Enabled     *bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		t, err := s.routines.Store().Patch(id, body.Name, body.Prompt, body.IntervalMin, body.Enabled)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, t)
	case http.MethodDelete:
		if err := s.routines.Store().Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{"deleted": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Routines is the subset of *routines.Service the server needs. Kept as an interface so
// the server package doesn't hard-depend on the scheduler's construction.
type Routines interface {
	Store() *routines.Store
	RunNow(ctx context.Context, id string) error
	ContextDigest() string
}
