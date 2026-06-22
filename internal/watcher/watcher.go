package watcher

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// SessionWatcher tails one session's .jsonl transcript from a given offset.
type SessionWatcher struct {
	SessionID string
	FilePath  string
	Offset    int64

	watcher *fsnotify.Watcher
	events  chan *ParsedMessage
	stop    chan struct{}
	mu      sync.RWMutex
}

// TranscriptPath returns the transcript path for a session id within projectsDir.
func TranscriptPath(projectsDir, claudeUUID string) string {
	return filepath.Join(projectsDir, claudeUUID+".jsonl")
}

// NewSessionWatcher starts watching the transcript at projectsDir/<claudeUUID>.jsonl,
// emitting only lines appended after creation (offset = current size).
func NewSessionWatcher(projectsDir, sessionID, claudeUUID string) (*SessionWatcher, error) {
	filePath := TranscriptPath(projectsDir, claudeUUID)

	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("session file not found: %w", err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	sw := &SessionWatcher{
		SessionID: sessionID,
		FilePath:  filePath,
		Offset:    info.Size(),
		watcher:   fsw,
		events:    make(chan *ParsedMessage, 256),
		stop:      make(chan struct{}),
	}

	if err := fsw.Add(filePath); err != nil {
		fsw.Close()
		return nil, fmt.Errorf("failed to watch file: %w", err)
	}

	log.Printf("[%s] watching transcript: %s (offset: %d)", sessionID, filePath, sw.Offset)
	return sw, nil
}

func (sw *SessionWatcher) Start() { go sw.watchLoop() }

func (sw *SessionWatcher) Stop() {
	close(sw.stop)
	sw.watcher.Close()
	close(sw.events)
}

func (sw *SessionWatcher) Events() <-chan *ParsedMessage { return sw.events }

func (sw *SessionWatcher) watchLoop() {
	for {
		select {
		case event, ok := <-sw.watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write {
				sw.handleFileChange()
			}
		case err, ok := <-sw.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[%s] watcher error: %v", sw.SessionID, err)
		case <-sw.stop:
			return
		}
	}
}

func (sw *SessionWatcher) handleFileChange() {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	file, err := os.Open(sw.FilePath)
	if err != nil {
		log.Printf("[%s] error opening transcript: %v", sw.SessionID, err)
		return
	}
	defer file.Close()

	if _, err := file.Seek(sw.Offset, 0); err != nil {
		log.Printf("[%s] error seeking transcript: %v", sw.SessionID, err)
		return
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		lineLen := int64(len(line)) + 1
		if line == "" {
			sw.Offset += lineLen
			continue
		}
		msg, err := ParseJSONLLine(line)
		if err != nil {
			sw.Offset += lineLen
			continue
		}
		parsed, err := ExtractContent(msg)
		if err == nil && parsed != nil {
			select {
			case sw.events <- parsed:
			case <-sw.stop:
				return
			}
		}
		sw.Offset += lineLen
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[%s] scanner error: %v", sw.SessionID, err)
	}
}
