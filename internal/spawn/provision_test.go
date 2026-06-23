package spawn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSpritesAPI emulates the part of the sprites API provisioning touches: a
// sprite that starts cold and warms after a couple of status polls, and a
// services endpoint that only persists a PUT once the sprite is warm — the exact
// behavior that broke the first provisioning attempt.
type fakeSpritesAPI struct {
	mu            sync.Mutex
	statusCalls   int
	warmAfter     int  // become warm after this many status GETs
	serviceStored bool // set by PUT only when warm
	putWhileCold  int
	putWhileWarm  int
}

func (f *fakeSpritesAPI) warm() bool {
	return f.statusCalls >= f.warmAfter
}

func (f *fakeSpritesAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/services/sprite-agent"):
			if f.serviceStored {
				w.Write([]byte(`{"name":"sprite-agent"}`))
			} else {
				http.Error(w, "service not found", http.StatusNotFound)
			}
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/services/sprite-agent"):
			if f.warm() {
				f.serviceStored = true
				f.putWhileWarm++
			} else {
				f.putWhileCold++ // 200 but does NOT persist (the real cold behavior)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet: // sprite status
			f.statusCalls++
			if f.warm() {
				w.Write([]byte(`{"status":"warm"}`))
			} else {
				w.Write([]byte(`{"status":"cold"}`))
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

func TestProvisionWaitsForWarmAndConfirms(t *testing.T) {
	fake := &fakeSpritesAPI{warmAfter: 2} // cold for the first status poll, then warm
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	a := &apiSpawner{
		base:         srv.URL,
		client:       &http.Client{Timeout: 5 * time.Second},
		pollInterval: time.Millisecond, // fast poll for the test
	}

	// warmSprite must block until the sprite reports non-cold.
	if err := a.warmSprite(context.Background(), "wk-test"); err != nil {
		t.Fatalf("warmSprite: %v", err)
	}
	fake.mu.Lock()
	warm := fake.warm()
	fake.mu.Unlock()
	if !warm {
		t.Fatal("expected sprite to be warm after warmSprite returned")
	}

	// A service PUT now persists, and serviceExists confirms it.
	if err := a.putService(context.Background(), "wk-test", []byte(`{}`)); err != nil {
		t.Fatalf("putService: %v", err)
	}
	if !a.serviceExists(context.Background(), "wk-test") {
		t.Fatal("serviceExists should be true after a warm PUT")
	}
}

func TestServiceExistsFalseWhenAbsent(t *testing.T) {
	fake := &fakeSpritesAPI{warmAfter: 0} // warm immediately, but nothing stored yet
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	a := &apiSpawner{base: srv.URL, client: &http.Client{Timeout: 5 * time.Second}}

	if a.serviceExists(context.Background(), "wk-test") {
		t.Fatal("serviceExists should be false before any PUT")
	}
}
