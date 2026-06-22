// Command smoke is a scripted end-to-end check of the chat loop (M2 acceptance):
// create a session over REST, open the WebSocket, send one message, and assert
// we observe token-level streaming (content_block_delta/text_delta) followed by
// a result. Exits non-zero on failure. Assumes the server is already running.
//
//	go run ./cmd/smoke -addr http://localhost:8080 -prompt "Say hello"
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	addr := flag.String("addr", "http://localhost:8080", "server base URL")
	prompt := flag.String("prompt", "Reply with exactly: SMOKE_OK", "message to send")
	timeout := flag.Duration("timeout", 120*time.Second, "overall timeout")
	flag.Parse()

	if err := run(*addr, *prompt, *timeout); err != nil {
		log.Fatalf("SMOKE FAIL: %v", err)
	}
	fmt.Println("SMOKE PASS")
}

func run(addr, prompt string, timeout time.Duration) error {
	// 1) create a session.
	id, err := createSession(addr)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	log.Printf("session: %s", id)

	// 2) open the WebSocket.
	wsURL, _ := url.Parse(addr)
	scheme := "ws"
	if wsURL.Scheme == "https" {
		scheme = "wss"
	}
	dialURL := fmt.Sprintf("%s://%s/ws?session=%s", scheme, wsURL.Host, id)
	conn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	// 3) send the message (after the connection settles so the process spawns).
	time.Sleep(1500 * time.Millisecond)
	if err := conn.WriteJSON(map[string]string{"type": "user", "content": prompt}); err != nil {
		return fmt.Errorf("ws write: %w", err)
	}
	log.Printf("sent: %q", prompt)

	// 4) collect events until result/timeout, asserting we saw streamed text.
	conn.SetReadDeadline(time.Now().Add(timeout))
	var sawDelta, sawResult bool
	var streamed strings.Builder
	for !sawResult {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read (sawDelta=%v): %w", sawDelta, err)
		}
		var msg struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "content_block_delta":
			if msg.Delta.Type == "text_delta" {
				sawDelta = true
				streamed.WriteString(msg.Delta.Text)
			}
		case "result":
			sawResult = true
		case "error":
			return fmt.Errorf("server error event: %s", string(data))
		}
	}

	if !sawDelta {
		return fmt.Errorf("never saw a token-level text_delta (streaming broken)")
	}
	log.Printf("streamed reply: %q", strings.TrimSpace(streamed.String()))
	return nil
}

func createSession(addr string) (string, error) {
	body, _ := json.Marshal(map[string]string{"name": "smoke"})
	resp, err := http.Post(addr+"/api/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	var s struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", err
	}
	if s.ID == "" {
		return "", fmt.Errorf("empty session id")
	}
	return s.ID, nil
}
