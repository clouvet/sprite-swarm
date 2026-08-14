package server

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeSpriteAccess(t *testing.T) {
	// Valid public request is delegated to the spawner.
	f := &fakeSpawner{}
	s := &Server{spawner: f}
	rec := httptest.NewRecorder()
	s.serveSpriteAccess(rec, httptest.NewRequest("POST", "/api/fleet/sprite-access",
		strings.NewReader(`{"target":"app-abc","visibility":"public"}`)))
	if rec.Code != 200 {
		t.Fatalf("public: code = %d", rec.Code)
	}
	if f.accessTarget != "app-abc" || f.accessVis != "public" {
		t.Errorf("delegated args = %q/%q", f.accessTarget, f.accessVis)
	}

	// Private with an explicit scope passes the scope through.
	f2 := &fakeSpawner{}
	s2 := &Server{spawner: f2}
	s2.serveSpriteAccess(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/fleet/sprite-access",
		strings.NewReader(`{"target":"wk-1","visibility":"private","scope":"org_users"}`)))
	if f2.accessVis != "private" || f2.accessScope != "org_users" {
		t.Errorf("private args = %q/%q", f2.accessVis, f2.accessScope)
	}

	// Missing visibility → 400 (not delegated).
	f3 := &fakeSpawner{}
	s3 := &Server{spawner: f3}
	rec3 := httptest.NewRecorder()
	s3.serveSpriteAccess(rec3, httptest.NewRequest("POST", "/api/fleet/sprite-access",
		strings.NewReader(`{"target":"wk-1"}`)))
	if rec3.Code != 400 || f3.accessTarget != "" {
		t.Errorf("missing visibility: code=%d target=%q", rec3.Code, f3.accessTarget)
	}

	// Spawner error → 502.
	f4 := &fakeSpawner{accessErr: errors.New("no such sprite: x")}
	s4 := &Server{spawner: f4}
	rec4 := httptest.NewRecorder()
	s4.serveSpriteAccess(rec4, httptest.NewRequest("POST", "/api/fleet/sprite-access",
		strings.NewReader(`{"target":"x","visibility":"public"}`)))
	if rec4.Code != 502 {
		t.Errorf("spawner error: code = %d", rec4.Code)
	}
}
