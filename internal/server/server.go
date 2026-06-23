// Package server is the HTTP front door: it serves the embedded PWA, the REST
// API (sessions, fleet), and the WebSocket endpoint, fronting the hub.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/clouvet/sprite-agent/internal/config"
	"github.com/clouvet/sprite-agent/internal/hub"
	"github.com/clouvet/sprite-agent/internal/spawn"
	"github.com/clouvet/sprite-agent/web"

	"github.com/gorilla/websocket"
)

// RosterProvider is the brain capability the HTTP layer needs (M4). Kept as an
// interface so the server doesn't depend on the concrete fleet package; main
// passes a *fleet.Service when a brain is configured, or nil otherwise.
type RosterProvider interface {
	Roster(ctx context.Context) (interface{}, error)
	MarkReapable(ctx context.Context) error
}

// Server wires the hub, session metadata, fleet brain, and HTTP routes.
type Server struct {
	cfg      config.Config
	hub      *hub.Hub
	store    *metaStore
	fleet    RosterProvider
	spawner  spawn.Spawner
	upgrader websocket.Upgrader
}

// New constructs a Server. fleetSvc may be nil if no brain is configured;
// spawner is always non-nil (a stub when no sprites token is available).
func New(cfg config.Config, h *hub.Hub, fleetSvc RosterProvider, spawner spawn.Spawner) *Server {
	return &Server{
		cfg:     cfg,
		hub:     h,
		store:   newMetaStore(filepath.Join(cfg.WorkDir, ".sprite-agent", "sessions.json")),
		fleet:   fleetSvc,
		spawner: spawner,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			// Same-origin in practice (served behind the sprite's private URL);
			// allow all so the PWA and reverse proxies connect cleanly.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Handler builds the HTTP routing mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/ws", s.serveWs)
	mux.HandleFunc("/health", s.serveHealth)
	mux.HandleFunc("/api/sessions", s.serveSessions)
	mux.HandleFunc("/api/sessions/", s.serveSessionByID)
	mux.HandleFunc("/api/fleet", s.serveFleet)
	mux.HandleFunc("/api/fleet/spawn", s.serveSpawn)
	mux.HandleFunc("/api/fleet/done", s.serveDone)

	// Static PWA from the embedded FS, with index fallback for the SPA root.
	fileServer := http.FileServer(http.FS(web.FS()))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fileServer.ServeHTTP(w, r)
	})

	return mux
}

func (s *Server) serveWs(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "session parameter required", http.StatusBadRequest)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}
	client := s.hub.NewClient(conn, sessionID, r.RemoteAddr)
	s.hub.RegisterClient(client)
	go client.WritePump()
	go client.ReadPump()
}

func (s *Server) serveHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(s.hub.HealthStatus())
}

// serveSessions handles GET (list) and POST (create) of sessions.
func (s *Server) serveSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.store.List())
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, s.store.Create(body.Name))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveSessionByID handles DELETE /api/sessions/{id} and the v1 update-id
// endpoint (a no-op under deterministic session ids, kept for client compat).
func (s *Server) serveSessionByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(rest, "/update-id") {
		// Deterministic ids: the web id already equals the Claude id. No-op.
		w.WriteHeader(http.StatusOK)
		return
	}
	id := rest
	if r.Method == http.MethodDelete {
		s.store.Delete(id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// serveFleet returns the roster (M4). 503 when no brain is configured.
func (s *Server) serveFleet(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	roster, err := s.fleet.Roster(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, roster)
}

// serveSpawn creates another sprite running this same artifact (M4). When no
// sprites token is configured the capability is addressable but returns 501 with
// a clear reason (the live call is stubbed; the interface is built).
func (s *Server) serveSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name       string            `json:"name"`
		NamePrefix string            `json:"name_prefix"`
		Role       string            `json:"role"`
		Labels     map[string]string `json:"labels"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !s.spawner.Available() {
		http.Error(w, spawn.ErrNotConfigured.Error(), http.StatusNotImplemented)
		return
	}
	res, err := s.spawner.Spawn(r.Context(), spawn.Request{
		Name: body.Name, NamePrefix: body.NamePrefix, Role: body.Role, Labels: body.Labels,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, res)
}

// serveDone marks this agent reapable (e.g. its task is finished / PR merged) so
// the fleet reaper destroys it. The agent does not destroy itself — a
// token-bearing reaper does, keeping the privileged token off workers.
func (s *Server) serveDone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.fleet.MarkReapable(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

var _ fs.FS = web.FS()
