package fleet

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeConnector emulates the S3 connector gateway: object GET/PUT/DELETE by path
// and ListObjectsV2 XML (with continuation). Locks connectorBrain's HTTP + XML
// handling so the token-free brain can't silently regress.
type fakeConnector struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (f *fakeConnector) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		// LIST: GET /?list-type=2&prefix=...
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			prefix := r.URL.Query().Get("prefix")
			var keys []string
			for k := range f.objs {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
			for _, k := range keys {
				fmt.Fprintf(&b, "<Contents><Key>%s</Key></Contents>", k)
			}
			b.WriteString(`</ListBucketResult>`)
			w.Write([]byte(b.String()))
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodPut:
			buf := make([]byte, r.ContentLength)
			r.Body.Read(buf)
			f.objs[key] = buf
			w.WriteHeader(200)
		case http.MethodGet:
			data, ok := f.objs[key]
			if !ok {
				http.Error(w, "NoSuchKey", http.StatusNotFound)
				return
			}
			w.Write(data)
		case http.MethodDelete:
			delete(f.objs, key)
			w.WriteHeader(204)
		}
	})
}

func TestConnectorBrainRoundTrip(t *testing.T) {
	fake := &fakeConnector{objs: map[string][]byte{}}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	b := newConnectorBrain(srv.URL)
	ctx := context.Background()

	// Put + Get.
	if err := b.Put(ctx, "fleet/wk-1/status.json", []byte(`{"id":"wk-1"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get(ctx, "fleet/wk-1/status.json")
	if err != nil || string(got) != `{"id":"wk-1"}` {
		t.Fatalf("get mismatch: %q %v", got, err)
	}
	// Missing key -> ErrNotFound.
	if _, err := b.Get(ctx, "fleet/missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	// List by prefix.
	b.Put(ctx, "fleet/wk-2/status.json", []byte(`{}`))
	b.Put(ctx, "other/x", []byte(`{}`))
	keys, err := b.List(ctx, "fleet/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 fleet/ keys, got %v", keys)
	}
	// Delete.
	if err := b.Delete(ctx, "fleet/wk-1/status.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(ctx, "fleet/wk-1/status.json"); err != ErrNotFound {
		t.Fatalf("expected deleted, got %v", err)
	}
}

// The connector brain satisfies the Brain interface (compile-time check).
var _ Brain = (*connectorBrain)(nil)
