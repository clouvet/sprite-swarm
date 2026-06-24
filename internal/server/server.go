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

// Fleet is the brain capability the HTTP layer needs. Kept as an interface so the
// server doesn't depend on the concrete fleet package; main passes a
// *fleet.Service when a brain is configured, or nil otherwise.
type Fleet interface {
	Roster(ctx context.Context) (interface{}, error)
	MarkReapable(ctx context.Context) error
	AgentPresent(ctx context.Context, id string) (exists, present bool, err error)
	RemoveAgent(ctx context.Context, id string) error
	Dispatch(ctx context.Context, target, task string) (interface{}, error)
	WriteMemoryValue(ctx context.Context, title, text string, tags []string) (interface{}, error)
	MemoryIndexValue(ctx context.Context) (interface{}, error)
	GetMemoryValue(ctx context.Context, author, id string) (interface{}, error)
	MemoryContext(ctx context.Context, limit int) (string, error)
	FleetContext(ctx context.Context, memLimit int) (string, error)
	EffectivePolicyValue(ctx context.Context) (interface{}, error)
	SpawnAllowed(ctx context.Context) (bool, string)
}

// Server wires the hub, session metadata, fleet brain, and HTTP routes.
type Server struct {
	cfg      config.Config
	hub      *hub.Hub
	store    *metaStore
	fleet    Fleet
	spawner  spawn.Spawner
	upgrader websocket.Upgrader
}

// New constructs a Server. fleetSvc may be nil if no brain is configured;
// spawner is always non-nil (a stub when no sprites token is available).
func New(cfg config.Config, h *hub.Hub, fleetSvc Fleet, spawner spawn.Spawner) *Server {
	store := newMetaStore(filepath.Join(cfg.WorkDir, ".sprite-agent", "sessions.json"))
	// Keep the session list's preview/timestamp fresh as turns happen.
	h.SetActivityHook(func(sessionID, preview string) {
		if len(preview) > 80 {
			preview = preview[:80]
		}
		store.Touch(sessionID, preview)
	})
	return &Server{
		cfg:     cfg,
		hub:     h,
		store:   store,
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
	mux.HandleFunc("/api/fleet/context", s.serveFleet)
	mux.HandleFunc("/api/fleet/spawn", s.serveSpawn)
	mux.HandleFunc("/api/fleet/done", s.serveDone)
	mux.HandleFunc("/api/fleet/dispatch", s.serveDispatch)
	mux.HandleFunc("/api/fleet/destroy", s.serveDestroy)
	mux.HandleFunc("/api/memory", s.serveMemory)
	mux.HandleFunc("/api/memory/", s.serveMemoryByPath)
	mux.HandleFunc("/api/policy", s.servePolicy)
	mux.HandleFunc("/api/config", s.serveConfig)
	mux.HandleFunc("/api/upload", s.serveUpload)
	mux.HandleFunc("/api/uploads/", s.serveUploadFile)

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
		// Merge stored sessions with live hub sessions, so dispatched sessions
		// (created by InjectMessage, not POST) are visible + attachable.
		list := s.store.List()
		seen := make(map[string]bool, len(list))
		for _, m := range list {
			seen[m.ID] = true
		}
		for _, hs := range s.hub.ListSessions() {
			if seen[hs.ID] {
				continue
			}
			name := "session " + shortID(hs.ID)
			preview := ""
			if hs.Generating {
				preview = "working…"
			}
			list = append(list, &SessionMeta{ID: hs.ID, Name: name, LastMessage: preview})
		}
		writeJSON(w, list)
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
	switch r.Method {
	case http.MethodDelete:
		s.store.Delete(id)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch, http.MethodPut:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		s.store.Rename(id, body.Name)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveFleet returns the roster (M4), or the live text context at
// /api/fleet/context (for per-turn prompt injection, P2.3). 503 with no brain.
func (s *Server) serveFleet(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/context") {
		text, err := s.fleet.FleetContext(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(text))
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
	// Default the worker name prefix so spawned sprites are clearly "wk-…" rather
	// than a bare random id.
	if body.Name == "" && body.NamePrefix == "" {
		body.NamePrefix = "wk-"
	}
	if !s.spawner.Available() {
		http.Error(w, spawn.ErrNotConfigured.Error(), http.StatusNotImplemented)
		return
	}
	// Enforce the capability policy's spawn cap (P2.5) before creating a sprite.
	if s.fleet != nil {
		if ok, reason := s.fleet.SpawnAllowed(r.Context()); !ok {
			http.Error(w, "policy: "+reason, http.StatusForbidden)
			return
		}
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

// serveDispatch assigns a task to another agent (P2.1). The task is recorded in
// the brain as visible fleet state; the target polls its inbox and injects it
// into its own session. Returns the task record (incl. the session id to attach).
func (s *Server) serveDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Target string `json:"target"`
		Task   string `json:"task"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Target == "" || body.Task == "" {
		http.Error(w, "target and task are required", http.StatusBadRequest)
		return
	}
	res, err := s.fleet.Dispatch(r.Context(), body.Target, body.Task)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, res)
}

// serveDestroy tears down a worker sprite (destroy VM + remove its brain entry).
// It honors presence (§2.4): if a human is attached to the target it refuses with
// 409 and a clear message unless {"force":true} is passed, so we never silently
// kill a session someone is actively steering. Refuses to destroy self.
func (s *Server) serveDestroy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Target string `json:"target"`
		Force  bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Target == "" {
		http.Error(w, "target is required", http.StatusBadRequest)
		return
	}
	if !s.spawner.Available() {
		http.Error(w, "no teardown capability on this sprite (no sprites API token)", http.StatusNotImplemented)
		return
	}
	if body.Target == s.cfg.AgentID {
		http.Error(w, "refusing to destroy self ("+body.Target+") — run this from another sprite", http.StatusConflict)
		return
	}
	exists, present, err := s.fleet.AgentPresent(r.Context(), body.Target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !exists {
		http.Error(w, "no such agent in the roster: "+body.Target, http.StatusNotFound)
		return
	}
	if present && !body.Force {
		http.Error(w, "DEFER: a human is attached to "+body.Target+
			". Re-POST with {\"force\":true} to destroy anyway.", http.StatusConflict)
		return
	}
	if err := s.spawner.Destroy(r.Context(), body.Target); err != nil {
		http.Error(w, "destroy failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	_ = s.fleet.RemoveAgent(r.Context(), body.Target) // best-effort brain cleanup
	writeJSON(w, map[string]interface{}{"destroyed": body.Target, "forced": body.Force})
}

// serveMemory: GET = the always-loaded index; POST = append a memory (P2.2).
func (s *Server) serveMemory(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		idx, err := s.fleet.MemoryIndexValue(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, idx)
	case http.MethodPost:
		var body struct {
			Title string   `json:"title"`
			Text  string   `json:"text"`
			Tags  []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}
		entry, err := s.fleet.WriteMemoryValue(r.Context(), body.Title, body.Text, body.Tags)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, entry)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveMemoryByPath: GET /api/memory/context (text index for prompt injection)
// or GET /api/memory/{author}/{id} (full entry, on-demand retrieval).
func (s *Server) serveMemoryByPath(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/memory/")
	if rest == "context" {
		text, err := s.fleet.MemoryContext(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(text))
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "want /api/memory/<author>/<id>", http.StatusBadRequest)
		return
	}
	entry, err := s.fleet.GetMemoryValue(r.Context(), parts[0], parts[1])
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, entry)
}

// servePolicy returns this agent's effective capability policy (P2.5 visibility).
// Read-only: agents never write fleet/config/* — that's human/control-plane held.
func (s *Server) servePolicy(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.Error(w, "fleet brain not configured", http.StatusServiceUnavailable)
		return
	}
	eff, err := s.fleet.EffectivePolicyValue(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, eff)
}

// RegisterSession gives a session a readable name in the list (used when a task
// is dispatched into a worker, so the work shows up labeled + attachable).
func (s *Server) RegisterSession(id, name string) {
	s.store.EnsureNamed(id, name)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

var _ fs.FS = web.FS()
