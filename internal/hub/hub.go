// Package hub fans one Claude session's stream out to N WebSocket clients and
// supervises the backing process. Lifted from claude-hub and adapted for v2:
// configurable working/projects dirs, scoped process options, deterministic
// session ids (no UUID-rename dance), and stream_event unwrapping for
// token-level streaming under --include-partial-messages.
package hub

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/clouvet/sprite-agent/internal/process"
	"github.com/clouvet/sprite-agent/internal/session"
	"github.com/clouvet/sprite-agent/internal/watcher"

	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
)

// Config holds the dirs and Claude driving options for the hub.
type Config struct {
	WorkDir        string
	ProjectsDir    string
	UploadsDir     string
	DangerousSkip  bool
	PermissionMode string
	SettingsPath   string
	MCPConfigPath  string
	AppendSystem   string
}

// Hub maintains active clients and broadcasts per-session.
type Hub struct {
	cfg providers

	sessions map[string]*session.Session
	clients  map[string]map[*Client]bool

	processMgr *process.Manager
	detector   *process.TerminalDetector
	watchers   map[string]*watcher.SessionWatcher

	register   chan *Client
	unregister chan *Client
	broadcast  chan *BroadcastMessage

	mu sync.RWMutex
}

// providers is the resolved hub configuration.
type providers struct {
	workDir        string
	projectsDir    string
	uploadsDir     string
	dangerousSkip  bool
	permissionMode string
	settingsPath   string
	mcpConfigPath  string
	appendSystem   string
}

// BroadcastMessage is a message to deliver to all clients of a session.
type BroadcastMessage struct {
	SessionID string
	Data      []byte
	Exclude   *Client
}

// NewHub creates a hub and starts watching the projects directory.
func NewHub(cfg Config) *Hub {
	h := &Hub{
		cfg: providers{
			workDir:        cfg.WorkDir,
			projectsDir:    cfg.ProjectsDir,
			uploadsDir:     cfg.UploadsDir,
			dangerousSkip:  cfg.DangerousSkip,
			permissionMode: cfg.PermissionMode,
			settingsPath:   cfg.SettingsPath,
			mcpConfigPath:  cfg.MCPConfigPath,
			appendSystem:   cfg.AppendSystem,
		},
		sessions:   make(map[string]*session.Session),
		clients:    make(map[string]map[*Client]bool),
		processMgr: process.NewManager(),
		detector:   process.NewTerminalDetector(cfg.ProjectsDir),
		watchers:   make(map[string]*watcher.SessionWatcher),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan *BroadcastMessage, 256),
	}
	go h.watchProjectsDirectory()
	return h
}

// NewClient builds a client for a WS connection.
func (h *Hub) NewClient(conn *websocket.Conn, sessionID, clientID string) *Client {
	return &Client{
		hub:       h,
		conn:      conn,
		send:      make(chan []byte, 256),
		sessionID: sessionID,
		clientID:  clientID,
	}
}

// RegisterClient enqueues a client for registration.
func (h *Hub) RegisterClient(client *Client) { h.register <- client }

// Run is the hub's main loop.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.registerClient(client)
		case client := <-h.unregister:
			h.unregisterClient(client)
		case message := <-h.broadcast:
			h.broadcastToSession(message)
		}
	}
}

func (h *Hub) spawnOpts(sessionID string) process.Options {
	return process.Options{
		SessionID:      sessionID,
		CWD:            h.cfg.workDir,
		ProjectsDir:    h.cfg.projectsDir,
		DangerousSkip:  h.cfg.dangerousSkip,
		PermissionMode: h.cfg.permissionMode,
		SettingsPath:   h.cfg.settingsPath,
		MCPConfigPath:  h.cfg.mcpConfigPath,
		AppendSystem:   h.cfg.appendSystem,
	}
}

func (h *Hub) registerClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.clients[client.sessionID] == nil {
		h.clients[client.sessionID] = make(map[*Client]bool)
	}
	h.clients[client.sessionID][client] = true
	h.processMgr.CancelGracePeriod(client.sessionID)

	sess := h.sessions[client.sessionID]
	if sess == nil {
		sess = session.NewSession(client.sessionID, h.cfg.workDir)
		// Deterministic ids: the transcript is <id>.jsonl. If it already exists
		// this is a resume (restart recovery / co-presence).
		if _, err := os.Stat(watcher.TranscriptPath(h.cfg.projectsDir, client.sessionID)); err == nil {
			log.Printf("session %s: found existing transcript, will resume", client.sessionID)
		} else {
			log.Printf("session %s: new", client.sessionID)
		}
		h.sessions[client.sessionID] = sess
	}
	sess.IncrementClients()

	log.Printf("client %s connected to %s (%d clients)", client.clientID, client.sessionID, len(h.clients[client.sessionID]))

	isGenerating := false
	if hp, err := h.processMgr.Get(client.sessionID); err == nil {
		isGenerating = hp.IsGenerating
	}

	if sess.ClaudeUUID != "" {
		go h.sendHistoryToClient(client, sess.ClaudeUUID, isGenerating)
	}

	if sess.GetClientCount() == 1 && sess.GetState() == session.StateIdle {
		go h.spawnClaudeForSession(client.sessionID, sess)
	}

	h.sendJSON(client, map[string]interface{}{
		"type":      "system",
		"message":   "Connected to sprite-agent",
		"sessionId": client.sessionID,
	})

	if isGenerating {
		h.sendJSON(client, map[string]interface{}{"type": "processing", "isProcessing": true})
	}
}

func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if set, ok := h.clients[client.sessionID]; ok {
		if _, ok := set[client]; ok {
			delete(set, client)
			close(client.send)
			if sess := h.sessions[client.sessionID]; sess != nil {
				sess.DecrementClients()
			}
			log.Printf("client %s disconnected from %s (%d remaining)", client.clientID, client.sessionID, len(set))
			if len(set) == 0 {
				delete(h.clients, client.sessionID)
				h.processMgr.StartGracePeriod(client.sessionID)
			}
		}
	}
}

func (h *Hub) broadcastToSession(message *BroadcastMessage) {
	h.mu.RLock()
	clients := h.clients[message.SessionID]
	h.mu.RUnlock()

	for client := range clients {
		if message.Exclude != nil && client == message.Exclude {
			continue
		}
		select {
		case client.send <- message.Data:
		default:
			close(client.send)
			h.mu.Lock()
			delete(h.clients[message.SessionID], client)
			h.mu.Unlock()
		}
	}
}

func (h *Hub) handleClientMessage(client *Client, msg *ClientMessage) {
	log.Printf("[%s] received %s from %s", client.sessionID, msg.Type, client.clientID)
	switch msg.Type {
	case "user":
		h.handleUserMessage(client, msg)
	case "interrupt":
		h.handleInterrupt(client)
	default:
		log.Printf("[%s] unknown message type: %s", client.sessionID, msg.Type)
	}
}

func (h *Hub) handleUserMessage(client *Client, msg *ClientMessage) {
	// Build the content sent to Claude: a content-block array when an image is
	// attached (image + text), otherwise the plain text string.
	content := h.buildContent(client.sessionID, msg)

	// Echo to other clients so co-present viewers see the user's turn (including
	// the image reference, so they can render the thumbnail).
	echo := map[string]interface{}{"role": "user", "content": msg.Content}
	if msg.ImageFilename != "" {
		echo["image"] = map[string]string{
			"id": msg.ImageID, "filename": msg.ImageFilename, "mediaType": msg.ImageMediaType,
		}
	}
	data, _ := json.Marshal(map[string]interface{}{"type": "user_message", "message": echo})
	h.broadcast <- &BroadcastMessage{SessionID: client.sessionID, Data: data, Exclude: client}

	sess := h.GetSession(client.sessionID)
	if sess != nil && sess.GetState() == session.StateIdle {
		log.Printf("[%s] no process, spawning before send", client.sessionID)
		h.spawnClaudeForSession(client.sessionID, sess)
		time.Sleep(500 * time.Millisecond)
	}

	if err := h.processMgr.SendMessage(client.sessionID, content); err != nil {
		log.Printf("[%s] send failed: %v; respawn+retry", client.sessionID, err)
		if sess != nil {
			h.spawnClaudeForSession(client.sessionID, sess)
			time.Sleep(500 * time.Millisecond)
			if err := h.processMgr.SendMessage(client.sessionID, content); err != nil {
				h.sendJSON(client, map[string]interface{}{"type": "error", "message": "Failed to send message to Claude: " + err.Error()})
				return
			}
		}
	}

	processingMsg, _ := json.Marshal(map[string]interface{}{"type": "processing", "isProcessing": true})
	h.broadcast <- &BroadcastMessage{SessionID: client.sessionID, Data: processingMsg}
}

// buildContent returns what to feed Claude as the user turn. With no image it's
// the plain text string. With an image, it's an Anthropic content-block array —
// the uploaded bytes (read from uploadsDir/<session>/<file>) base64-encoded into an
// image block, plus a text block. Falls back to text if the file can't be read.
func (h *Hub) buildContent(sessionID string, msg *ClientMessage) interface{} {
	if msg.ImageFilename == "" || h.cfg.uploadsDir == "" {
		return msg.Content
	}
	path := filepath.Join(h.cfg.uploadsDir, sessionID, filepath.Base(msg.ImageFilename))
	bytes, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[%s] image read failed (%v); sending text only", sessionID, err)
		return msg.Content
	}
	media := msg.ImageMediaType
	if media == "" {
		media = "image/png"
	}
	blocks := []map[string]interface{}{{
		"type": "image",
		"source": map[string]interface{}{
			"type":       "base64",
			"media_type": media,
			"data":       base64.StdEncoding.EncodeToString(bytes),
		},
	}}
	if msg.Content != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": msg.Content})
	}
	return blocks
}

func (h *Hub) handleInterrupt(client *Client) {
	log.Printf("[%s] interrupt", client.sessionID)
	if err := h.processMgr.Kill(client.sessionID); err != nil {
		log.Printf("[%s] interrupt error: %v", client.sessionID, err)
	}
	resultMsg, _ := json.Marshal(map[string]interface{}{"type": "result"})
	h.broadcast <- &BroadcastMessage{SessionID: client.sessionID, Data: resultMsg}
	if sess := h.GetSession(client.sessionID); sess != nil {
		go h.spawnClaudeForSession(client.sessionID, sess)
	}
}

// InjectMessage delivers a message into a local session as if a user sent it —
// the path a dispatched task takes (P2.1): a worker pulls a task from the brain
// and injects it here, so it materializes in the session's transcript and drives
// Claude exactly like a human message would (seam #2). Creates/spawns as needed.
func (h *Hub) InjectMessage(sessionID, content string) error {
	h.mu.Lock()
	sess := h.sessions[sessionID]
	if sess == nil {
		sess = session.NewSession(sessionID, h.cfg.workDir)
		h.sessions[sessionID] = sess
	}
	h.mu.Unlock()

	if sess.GetState() == session.StateIdle {
		h.spawnClaudeForSession(sessionID, sess)
		time.Sleep(500 * time.Millisecond)
	}

	// Show the injected turn to any attached clients.
	userMsg, _ := json.Marshal(map[string]interface{}{
		"type":    "user_message",
		"message": map[string]interface{}{"role": "user", "content": content},
	})
	h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: userMsg}

	if err := h.processMgr.SendMessage(sessionID, content); err != nil {
		h.spawnClaudeForSession(sessionID, sess)
		time.Sleep(500 * time.Millisecond)
		if err := h.processMgr.SendMessage(sessionID, content); err != nil {
			return err
		}
	}
	processingMsg, _ := json.Marshal(map[string]interface{}{"type": "processing", "isProcessing": true})
	h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: processingMsg}
	return nil
}

// GetSession returns a session by id.
func (h *Hub) GetSession(sessionID string) *session.Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[sessionID]
}

func (h *Hub) spawnClaudeForSession(sessionID string, sess *session.Session) {
	log.Printf("[%s] spawning headless claude", sessionID)
	h.stopFileWatching(sessionID)

	hp, err := h.processMgr.Spawn(h.spawnOpts(sessionID))
	if err != nil {
		log.Printf("[%s] spawn failed: %v", sessionID, err)
		errMsg, _ := json.Marshal(map[string]interface{}{"type": "error", "message": "Failed to start Claude process"})
		h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: errMsg}
		return
	}
	sess.SetState(session.StateWebOnly)
	go h.handleClaudeOutput(sessionID, hp)
}

// eventType peeks the "type" field of a raw JSON object.
func eventType(raw json.RawMessage) string {
	var probe struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Type
}

// handleClaudeOutput forwards Claude's stream to clients. stream_event envelopes
// are unwrapped so the UI sees top-level content_block_* / message_* events; the
// redundant full "assistant" message is dropped (deltas already render it).
func (h *Hub) handleClaudeOutput(sessionID string, hp *process.HeadlessProcess) {
	sess := h.GetSession(sessionID)

	if hp.Cmd != nil && hp.Cmd.Process != nil {
		h.detector.RegisterOwnPID(hp.Cmd.Process.Pid)
		defer h.detector.UnregisterOwnPID(hp.Cmd.Process.Pid)
	}

	for {
		select {
		case msg, ok := <-hp.OutputChan:
			if !ok {
				log.Printf("[%s] output channel closed", sessionID)
				return
			}

			// Stream events: forward the inner event, track streaming state.
			if msg.IsStreamEvent() {
				inner := msg.Event
				switch eventType(inner) {
				case "content_block_start":
					if sess != nil {
						sess.AddContentBlock(inner)
						sess.SetGenerating(true)
					}
				case "message_stop":
					if sess != nil {
						sess.ClearContentBlocks()
						sess.SetGenerating(false)
					}
				}
				h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: inner}
				continue
			}

			switch msg.Type {
			case "assistant":
				// Redundant with streamed deltas — drop to avoid double render.
				continue
			case "system":
				if msg.Subtype == "init" && msg.SessionID != "" && sess != nil {
					sess.SetClaudeUUID(msg.SessionID)
				}
			case "result":
				if sess != nil {
					sess.SetGenerating(false)
				}
			}

			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: data}

		case err, ok := <-hp.ErrorChan:
			if !ok {
				return
			}
			log.Printf("[%s] process error: %v", sessionID, err)
			errMsg, _ := json.Marshal(map[string]interface{}{"type": "error", "message": err.Error()})
			h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: errMsg}

		case <-hp.Done():
			log.Printf("[%s] process context cancelled", sessionID)
			return
		}
	}
}

// --- terminal co-presence: watch the projects dir for terminal-driven writes ---

func (h *Hub) watchProjectsDirectory() {
	dirWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("failed to create dir watcher: %v", err)
		return
	}
	defer dirWatcher.Close()

	if err := os.MkdirAll(h.cfg.projectsDir, 0o755); err != nil {
		log.Printf("failed to ensure projects dir: %v", err)
	}
	if err := dirWatcher.Add(h.cfg.projectsDir); err != nil {
		log.Printf("failed to watch projects dir: %v", err)
		return
	}
	log.Printf("watching projects dir: %s", h.cfg.projectsDir)

	for {
		select {
		case event, ok := <-dirWatcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write && strings.HasSuffix(event.Name, ".jsonl") {
				h.handleProjectFileChange(event.Name)
			}
		case err, ok := <-dirWatcher.Errors:
			if !ok {
				return
			}
			log.Printf("dir watcher error: %v", err)
		}
	}
}

func (h *Hub) handleProjectFileChange(filePath string) {
	claudeUUID := strings.TrimSuffix(filepath.Base(filePath), ".jsonl")

	h.mu.Lock()
	defer h.mu.Unlock()

	var sess *session.Session
	var sessionID string
	for id, s := range h.sessions {
		if s.ClaudeUUID == claudeUUID {
			sess, sessionID = s, id
			break
		}
	}
	if sess == nil {
		return
	}

	hp, err := h.processMgr.Get(sessionID)
	hasHeadless := err == nil
	state := sess.GetState()

	// A transcript write while our headless process is not generating means a
	// terminal `claude --resume` session is driving it: switch to watching.
	if state == session.StateWebOnly && hasHeadless && !hp.IsGenerating {
		log.Printf("[%s] transcript changed while idle — terminal session likely", sessionID)
		h.processMgr.Kill(sessionID)
		sess.TransitionTo(session.StateTerminalOnly)
		h.startFileWatchingLocked(sessionID, sess)
		h.notifyLocked(sessionID, "Terminal session detected — file watching active")
	} else if !hasHeadless && state != session.StateTerminalOnly {
		sess.TransitionTo(session.StateTerminalOnly)
		h.startFileWatchingLocked(sessionID, sess)
		h.notifyLocked(sessionID, "Terminal session detected — file watching active")
	}
}

func (h *Hub) startFileWatchingLocked(sessionID string, sess *session.Session) {
	if h.watchers[sessionID] != nil || sess.ClaudeUUID == "" {
		return
	}
	w, err := watcher.NewSessionWatcher(h.cfg.projectsDir, sessionID, sess.ClaudeUUID)
	if err != nil {
		log.Printf("[%s] failed to create watcher: %v", sessionID, err)
		return
	}
	h.watchers[sessionID] = w
	w.Start()
	go h.handleWatcherEvents(sessionID, w)
}

func (h *Hub) stopFileWatching(sessionID string) {
	h.mu.Lock()
	w := h.watchers[sessionID]
	delete(h.watchers, sessionID)
	h.mu.Unlock()
	if w != nil {
		w.Stop()
		log.Printf("[%s] stopped file watching", sessionID)
	}
}

func (h *Hub) handleWatcherEvents(sessionID string, w *watcher.SessionWatcher) {
	for event := range w.Events() {
		switch event.Role {
		case "user":
			data, _ := json.Marshal(map[string]interface{}{
				"type": "user_message",
				"message": map[string]interface{}{
					"role":      "user",
					"content":   event.Content,
					"timestamp": event.Timestamp.Unix() * 1000,
				},
			})
			h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: data}
		case "assistant":
			data, _ := json.Marshal(map[string]interface{}{
				"type": "assistant",
				"message": map[string]interface{}{
					"role":      "assistant",
					"content":   event.Content,
					"timestamp": event.Timestamp.Unix() * 1000,
				},
			})
			h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: data}
			resultData, _ := json.Marshal(map[string]interface{}{"type": "result"})
			h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: resultData}
		}
	}
}

func (h *Hub) notifyLocked(sessionID, message string) {
	data, _ := json.Marshal(map[string]interface{}{"type": "system", "message": message})
	h.broadcast <- &BroadcastMessage{SessionID: sessionID, Data: data}
}

// sendHistoryToClient replays the transcript to a newly connected client.
func (h *Hub) sendHistoryToClient(client *Client, claudeUUID string, isGenerating bool) {
	filePath := watcher.TranscriptPath(h.cfg.projectsDir, claudeUUID)
	file, err := os.Open(filePath)
	if err != nil {
		return
	}
	defer file.Close()

	messages := []map[string]interface{}{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		msg, err := watcher.ParseJSONLLine(line)
		if err != nil {
			continue
		}
		parsed, err := watcher.ExtractContent(msg)
		if err != nil || parsed == nil {
			continue
		}
		messages = append(messages, map[string]interface{}{
			"role":      parsed.Role,
			"content":   parsed.Content,
			"timestamp": parsed.Timestamp.Unix() * 1000,
		})
	}

	if len(messages) > 0 || isGenerating {
		h.sendJSON(client, map[string]interface{}{
			"type":         "history",
			"messages":     messages,
			"isGenerating": isGenerating,
		})
	}

	if isGenerating {
		if sess := h.GetSession(client.sessionID); sess != nil {
			for _, block := range sess.GetStreamingState().ActiveContentBlocks {
				select {
				case client.send <- block:
				default:
				}
			}
		}
	}
}

// sendJSON marshals v and sends it to one client (best-effort).
func (h *Hub) sendJSON(client *Client, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case client.send <- data:
	default:
	}
}

// SessionInfo is a hub session for the session-list UI.
type SessionInfo struct {
	ID         string
	Generating bool
}

// ListSessions returns every session the hub knows (active or with a transcript),
// so dispatched sessions — created by InjectMessage, not by POST /api/sessions —
// are visible and attachable in the UI rather than hidden.
func (h *Hub) ListSessions() []SessionInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]SessionInfo, 0, len(h.sessions))
	for id := range h.sessions {
		gen := false
		if hp, err := h.processMgr.Get(id); err == nil {
			gen = hp.IsGenerating
		}
		out = append(out, SessionInfo{ID: id, Generating: gen})
	}
	return out
}

// IsIdle reports whether the agent has no connected clients and nothing
// generating — used by the fleet to decide a worker is reapable after a while.
func (h *Hub) IsIdle() bool {
	if h.processMgr.GetActiveGeneratingCount() > 0 {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, clients := range h.clients {
		if len(clients) > 0 {
			return false
		}
	}
	return true
}

// Attendance reports whether a human is attached (a client is connected) and to
// which session — the presence signal (DESIGN §2.4). Used to advertise "a human
// is here" to the fleet so other surfaces defer.
func (h *Hub) Attendance() (bool, string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sid, clients := range h.clients {
		if len(clients) > 0 {
			return true, sid
		}
	}
	return false, ""
}

// HealthStatus reports liveness + whether the sprite should stay awake.
func (h *Hub) HealthStatus() []byte {
	h.mu.RLock()
	defer h.mu.RUnlock()

	generating := h.processMgr.GetActiveGeneratingCount()
	connections := 0
	for _, clients := range h.clients {
		connections += len(clients)
	}
	status := map[string]interface{}{
		"status":             "ok",
		"active_sessions":    len(h.sessions),
		"active_processes":   h.processMgr.GetProcessCount(),
		"generating":         generating,
		"active_connections": connections,
		"keep_sprite_awake":  generating > 0,
	}
	data, _ := json.Marshal(status)
	return data
}
