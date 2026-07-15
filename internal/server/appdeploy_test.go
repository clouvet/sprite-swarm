package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestServeDestroyApp(t *testing.T) {
	f := &fakeSpawner{taken: map[string]bool{"app-abc123": true}}
	s := &Server{spawner: f, cfg: config.Config{AgentID: "home"}}

	// Existing app sprite → destroyed (no roster membership required).
	rec := httptest.NewRecorder()
	s.serveDestroyApp(rec, httptest.NewRequest("POST", "/api/fleet/destroy-app", strings.NewReader(`{"name":"app-abc123"}`)))
	if rec.Code != 200 {
		t.Fatalf("destroy existing app: code %d, body %s", rec.Code, rec.Body)
	}
	if len(f.destroyed) != 1 || f.destroyed[0] != "app-abc123" {
		t.Errorf("expected app-abc123 destroyed, got %v", f.destroyed)
	}

	// Unknown sprite → 404, nothing destroyed.
	rec = httptest.NewRecorder()
	s.serveDestroyApp(rec, httptest.NewRequest("POST", "/api/fleet/destroy-app", strings.NewReader(`{"name":"app-nope"}`)))
	if rec.Code != 404 {
		t.Errorf("unknown app: want 404, got %d", rec.Code)
	}

	// Missing name → 400.
	rec = httptest.NewRecorder()
	s.serveDestroyApp(rec, httptest.NewRequest("POST", "/api/fleet/destroy-app", strings.NewReader(`{}`)))
	if rec.Code != 400 {
		t.Errorf("missing name: want 400, got %d", rec.Code)
	}

	// Refuse to destroy self.
	rec = httptest.NewRecorder()
	s.serveDestroyApp(rec, httptest.NewRequest("POST", "/api/fleet/destroy-app", strings.NewReader(`{"name":"home"}`)))
	if rec.Code != 409 {
		t.Errorf("destroy self: want 409, got %d", rec.Code)
	}
}

func TestServeUpdateApp(t *testing.T) {
	f := &fakeSpawner{taken: map[string]bool{"app-abc123": true}}
	s := &Server{spawner: f, cfg: config.Config{AgentID: "home"}}

	// Full payload → update dispatched to the named sprite.
	rec := httptest.NewRecorder()
	body := `{"name":"app-abc123","artifact_url":"https://brain/x.tgz","run":"./serve","http_port":3000}`
	s.serveUpdateApp(rec, httptest.NewRequest("POST", "/api/fleet/update-app", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("update: code %d, body %s", rec.Code, rec.Body)
	}
	if f.updatedName != "app-abc123" || f.updatedReq.HTTPPort != 3000 || f.updatedReq.Run != "./serve" {
		t.Errorf("update not passed through: name=%q req=%+v", f.updatedName, f.updatedReq)
	}

	// Missing fields → 400 (name present but no artifact/run/port).
	rec = httptest.NewRecorder()
	s.serveUpdateApp(rec, httptest.NewRequest("POST", "/api/fleet/update-app", strings.NewReader(`{"name":"app-abc123"}`)))
	if rec.Code != 400 {
		t.Errorf("missing fields: want 400, got %d", rec.Code)
	}
}
