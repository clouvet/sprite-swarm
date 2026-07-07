package hub

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512 * 1024
)

// Client is a WebSocket client attached to a session.
type Client struct {
	hub       *Hub
	conn      *websocket.Conn
	send      chan []byte
	sessionID string
	clientID  string
}

// Attachment references one uploaded file. The client uploads the bytes to
// /api/upload first and sends only these references; the hub reads the file and
// feeds it to Claude (image block, inlined text, or a saved path for binary docs).
type Attachment struct {
	ID   string `json:"id,omitempty"`
	File string `json:"file,omitempty"` // stored filename
	Name string `json:"name,omitempty"` // original filename
	Type string `json:"type,omitempty"` // media type
}

// ClientMessage is an inbound message from a web client.
type ClientMessage struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	// Attachments the client sends with this turn (may be several files at once).
	Attachments []Attachment `json:"attachments,omitempty"`
	// Deprecated single-attachment fields, still read for backward compatibility
	// with older clients. Prefer Attachments; see allAttachments.
	AttachmentID   string `json:"attachmentId,omitempty"`
	AttachmentFile string `json:"attachmentFile,omitempty"`
	AttachmentName string `json:"attachmentName,omitempty"`
	AttachmentType string `json:"attachmentType,omitempty"`
}

// allAttachments returns the turn's attachments, normalizing the legacy single
// fields into the slice form so callers only handle one shape.
func (m *ClientMessage) allAttachments() []Attachment {
	if len(m.Attachments) > 0 {
		return m.Attachments
	}
	if m.AttachmentFile != "" {
		return []Attachment{{ID: m.AttachmentID, File: m.AttachmentFile, Name: m.AttachmentName, Type: m.AttachmentType}}
	}
	return nil
}

// ReadPump pumps client messages into the hub.
func (c *Client) ReadPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	c.conn.SetReadLimit(maxMessageSize)

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("websocket error: %v", err)
			}
			break
		}
		var msg ClientMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("parse client message: %v", err)
			continue
		}
		c.hub.handleClientMessage(c, &msg)
	}
}

// WritePump pumps hub broadcasts to the client.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
