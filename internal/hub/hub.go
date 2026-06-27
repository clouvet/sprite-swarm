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

	"github.com/clouvet/sprite-agent/internal/config"
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

	// onActivity, when set, is called with a session id + short preview whenever a
	// turn happens, so the session list (lastMessage/lastMessageAt) stays current.
	onActivity func(sessionID, preview string)

	mu sync.RWMutex
}

// SetActivityHook registers a callback invoked on each user turn so the session
// store can update its preview/timestamp (wired by the server to metaStore.Touch).
func (h *Hub) SetActivityHook(fn func(sessionID, preview string)) { h.onActivity = fn }

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

// sessionCWD is the per-chat working directory: an isolated subdir under the base
// workdir so concurrent chats don't clobber each other's files. Shared context
// flows through the fleet brain, not the filesystem. Everything lives under the
// base workdir (/home/sprite) so a human who logs into the sprite can inspect it.
func (h *Hub) sessionCWD(id string) string {
	return filepath.Join(h.cfg.workDir, "chats", id)
}

// sessionProjectsDir is where Claude writes this session's transcript (derived
// from its per-session cwd), used for history replay and the resume decision.
func (h *Hub) sessionProjectsDir(id string) string {
	return config.ProjectsDirFor(h.sessionCWD(id))
}

func (h *Hub) spawnOpts(sessionID string) process.Options {
	cwd := h.sessionCWD(sessionID)
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		log.Printf("[%s] mkdir chat workdir failed: %v", sessionID, err)
	}
	return process.Options{
		SessionID:      sessionID,
		CWD:            cwd,
		ProjectsDir:    h.sessionProjectsDir(sessionID),
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
		sess = session.NewSession(client.sessionID, h.sessionCWD(client.sessionID))
		// Deterministic ids: the transcript is <id>.jsonl. If it already exists
		// this is a resume (restart recovery / co-presence).
		if _, err := os.Stat(watcher.TranscriptPath(h.sessionProjectsDir(client.sessionID), client.sessionID)); err == nil {
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
	// the attachment reference, so they can render the thumbnail / file chip).
	echo := map[string]interface{}{"role": "user", "content": msg.Content}
	if msg.AttachmentFile != "" {
		echo["attachment"] = map[string]string{
			"id": msg.AttachmentID, "file": msg.AttachmentFile, "name": msg.AttachmentName, "type": msg.AttachmentType,
		}
	}
	data, _ := json.Marshal(map[string]interface{}{"type": "user_message", "message": echo})
	h.broadcast <- &BroadcastMessage{SessionID: client.sessionID, Data: data, Exclude: client}

	// Keep the session list's preview/timestamp current.
	if h.onActivity != nil {
		preview := msg.Content
		if preview == "" && msg.AttachmentFile != "" {
			preview = "📎 " + msg.AttachmentName
		}
		h.onActivity(client.sessionID, preview)
	}

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

// maxInlineFile caps how much of a text attachment we inline into the turn.
const maxInlineFile = 256 * 1024

// buildContent returns what to feed Claude as the user turn:
//   - no attachment        → the plain text string
//   - image                → content-block array: image (base64) + text
//   - text-like (text/*)   → text with the file's contents inlined
//   - other (binary docs)  → text noting the saved path; the agent reads/converts it
//
// Files are read from uploadsDir/<session>/<file>. Falls back to plain text on error.
func (h *Hub) buildContent(sessionID string, msg *ClientMessage) interface{} {
	if msg.AttachmentFile == "" || h.cfg.uploadsDir == "" {
		return msg.Content
	}
	path := filepath.Join(h.cfg.uploadsDir, sessionID, filepath.Base(msg.AttachmentFile))
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[%s] attachment read failed (%v); sending text only", sessionID, err)
		return msg.Content
	}
	name := msg.AttachmentName
	if name == "" {
		name = msg.AttachmentFile
	}
	media := msg.AttachmentType

	// Image → native image content block.
	if strings.HasPrefix(media, "image/") {
		blocks := []map[string]interface{}{{
			"type": "image",
			"source": map[string]interface{}{
				"type": "base64", "media_type": media, "data": base64.StdEncoding.EncodeToString(data),
			},
		}}
		if msg.Content != "" {
			blocks = append(blocks, map[string]interface{}{"type": "text", "text": msg.Content})
		}
		return blocks
	}

	// Text-like → inline the contents directly.
	if strings.HasPrefix(media, "text/") && len(data) <= maxInlineFile {
		return msg.Content + "\n\n--- Attached file: " + name + " ---\n" + string(data)
	}

	// Binary docs → point at the saved file; the agent (full tools) reads/converts it.
	return msg.Content + "\n\n[Attached file \"" + name + "\" saved at " + path +
		" — read or convert it with your tools to use its contents.]"
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
		sess = session.NewSession(sessionID, h.sessionCWD(sessionID))
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
	} else if !hasHeadless && state != session.StateTerminalOnly {
		sess.TransitionTo(session.StateTerminalOnly)
		h.startFileWatchingLocked(sessionID, sess)
	}
}

func (h *Hub) startFileWatchingLocked(sessionID string, sess *session.Session) {
	if h.watchers[sessionID] != nil || sess.ClaudeUUID == "" {
		return
	}
	w, err := watcher.NewSessionWatcher(h.sessionProjectsDir(sessionID), sessionID, sess.ClaudeUUID)
	if err != nil {
		log.Printf("[%s] failed to create watcher: %v", sessionID, err)
		return
	}
	h.watchers[sessionID] = w
	w.Start()
	go h.handleWatcherEvents(sessionID, w)
}

// RemoveSession evicts a session from the hub entirely (kills its process, stops
// its watcher, drops it from the session/client maps), so a deleted chat doesn't
// linger and reappear via the /api/sessions hub-merge after a refresh.
func (h *Hub) RemoveSession(sessionID string) {
	_ = h.processMgr.Kill(sessionID) // best-effort; own lock
	h.stopFileWatching(sessionID)    // own lock
	h.mu.Lock()
	delete(h.sessions, sessionID)
	delete(h.clients, sessionID)
	h.mu.Unlock()
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
	filePath := watcher.TranscriptPath(h.sessionProjectsDir(client.sessionID), claudeUUID)
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
			"images":    parsed.Images,
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

// SessionResult returns a session's final assistant message — its "result" —
// read from the transcript on disk. This backs the result-pull endpoint: a peer
// (home) fetches what a worker produced from the exact session it dispatched to,
// so the worker never has to push anything back. ok is false if there's no
// transcript or no assistant text yet.
func (h *Hub) SessionResult(sessionID string) (text string, tsMillis int64, ok bool) {
	claudeUUID := sessionID
	if sess := h.GetSession(sessionID); sess != nil && sess.ClaudeUUID != "" {
		claudeUUID = sess.ClaudeUUID
	}
	file, err := os.Open(watcher.TranscriptPath(h.sessionProjectsDir(sessionID), claudeUUID))
	if err != nil {
		return "", 0, false
	}
	defer file.Close()

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
		if parsed.Role == "assistant" && strings.TrimSpace(parsed.Content) != "" {
			text = parsed.Content
			tsMillis = parsed.Timestamp.Unix() * 1000
			ok = true
		}
	}
	return text, tsMillis, ok
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
// Generating reports whether any session is currently generating — used to
// serialize dispatched work (don't start a new dispatched task while one runs).
func (h *Hub) Generating() bool { return h.processMgr.GetActiveGeneratingCount() > 0 }

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
