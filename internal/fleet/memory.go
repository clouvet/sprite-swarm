package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Durable shared memory (DESIGN §4 Layer 2, §9): v1's session-memories promoted
// to fleet memory. Append-only, collision-proof per-key, under a dedicated
// top-level prefix so it never collides with per-agent coordination keys AND
// survives reaping (RemoveAgent only touches status+heartbeat).
//
// Scaling rule (§4): keep a small always-loaded index + retrieve full entries on
// demand. MemoryIndex returns headers (no body); GetMemory fetches one body.

// MemoryEntry is one durable learning/decision/artifact written by an agent.
type MemoryEntry struct {
	ID        string   `json:"id"`
	Author    string   `json:"author"`
	Title     string   `json:"title"`
	Text      string   `json:"text"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt int64    `json:"created_at"`
}

// MemoryHeader is the index view (no body) — what's cheap to always load.
type MemoryHeader struct {
	ID        string   `json:"id"`
	Author    string   `json:"author"`
	Title     string   `json:"title"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt int64    `json:"created_at"`
}

func memoryAuthorPrefix(author string) string { return path.Join("fleet", "memory", author) + "/" }
func memoryKey(author, id string) string      { return path.Join("fleet", "memory", author, id+".json") }

// WriteMemory appends a durable memory authored by this agent (its own prefix).
func (s *Service) WriteMemory(ctx context.Context, title, text string, tags []string) (MemoryEntry, error) {
	now := s.now()
	e := MemoryEntry{
		ID:        timestampID(now) + "-" + newUUID(),
		Author:    s.id,
		Title:     title,
		Text:      text,
		Tags:      tags,
		CreatedAt: now.Unix(),
	}
	data, _ := json.Marshal(e)
	if err := s.brain.Put(ctx, memoryKey(s.id, e.ID), data); err != nil {
		return MemoryEntry{}, err
	}
	return e, nil
}

// MemoryIndex lists memory headers across all authors, newest first. This is the
// always-loaded index; bodies are fetched on demand via GetMemory.
func (s *Service) MemoryIndex(ctx context.Context) ([]MemoryHeader, error) {
	keys, err := s.brain.List(ctx, "fleet/memory/")
	if err != nil {
		return nil, err
	}
	var headers []MemoryHeader
	for _, k := range keys {
		if !strings.HasSuffix(k, ".json") {
			continue
		}
		data, err := s.brain.Get(ctx, k)
		if err != nil {
			continue
		}
		var e MemoryEntry
		if json.Unmarshal(data, &e) != nil {
			continue
		}
		headers = append(headers, MemoryHeader{
			ID: e.ID, Author: e.Author, Title: e.Title, Tags: e.Tags, CreatedAt: e.CreatedAt,
		})
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].CreatedAt > headers[j].CreatedAt })
	return headers, nil
}

// GetMemory fetches a full entry by author + id (on-demand retrieval).
func (s *Service) GetMemory(ctx context.Context, author, id string) (MemoryEntry, error) {
	data, err := s.brain.Get(ctx, memoryKey(author, id))
	if err != nil {
		return MemoryEntry{}, err
	}
	var e MemoryEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return MemoryEntry{}, err
	}
	return e, nil
}

// MemoryContext renders the index as a compact text block for injection into an
// agent's context each turn (the "always-loaded index", §4). Bodies are omitted;
// the agent retrieves the ones it needs via GET /api/memory/<author>/<id>.
func (s *Service) MemoryContext(ctx context.Context, limit int) (string, error) {
	headers, err := s.MemoryIndex(ctx)
	if err != nil {
		return "", err
	}
	if len(headers) == 0 {
		return "", nil
	}
	if limit > 0 && len(headers) > limit {
		headers = headers[:limit]
	}
	var b strings.Builder
	b.WriteString("## Fleet memory (shared, durable) — index only; retrieve a body with GET /api/memory/<author>/<id>\n")
	for _, h := range headers {
		tags := ""
		if len(h.Tags) > 0 {
			tags = " [" + strings.Join(h.Tags, ",") + "]"
		}
		fmt.Fprintf(&b, "- %s/%s — %s%s\n", h.Author, h.ID, h.Title, tags)
	}
	return b.String(), nil
}

// MemoryIndexValue is the any-returning wrapper for the HTTP layer.
func (s *Service) MemoryIndexValue(ctx context.Context) (interface{}, error) {
	return s.MemoryIndex(ctx)
}

// WriteMemoryValue is the any-returning wrapper for the HTTP layer.
func (s *Service) WriteMemoryValue(ctx context.Context, title, text string, tags []string) (interface{}, error) {
	return s.WriteMemory(ctx, title, text, tags)
}

// GetMemoryValue is the any-returning wrapper for the HTTP layer.
func (s *Service) GetMemoryValue(ctx context.Context, author, id string) (interface{}, error) {
	return s.GetMemory(ctx, author, id)
}
