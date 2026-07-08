package secret

import (
	"reflect"
	"strings"
	"testing"
)

func TestStoreSetOverwriteDelete(t *testing.T) {
	s := NewStore()
	s.Set("B_KEY", "one")
	s.Set("A_KEY", "two")
	if got := s.Names(); !reflect.DeepEqual(got, []string{"A_KEY", "B_KEY"}) {
		t.Fatalf("Names sorted = %v", got)
	}
	// Overwrite is an upsert, not a duplicate.
	s.Set("A_KEY", "two-updated")
	if got := s.Names(); !reflect.DeepEqual(got, []string{"A_KEY", "B_KEY"}) {
		t.Fatalf("overwrite changed name set: %v", got)
	}
	env := s.Env()
	if !contains(env, "A_KEY=two-updated") || !contains(env, "B_KEY=one") {
		t.Fatalf("Env = %v", env)
	}
	s.Delete("A_KEY")
	if got := s.Names(); !reflect.DeepEqual(got, []string{"B_KEY"}) {
		t.Fatalf("after delete = %v", got)
	}
}

func TestStoreMask(t *testing.T) {
	s := NewStore()
	s.Set("TOKEN", "supersecretvalue")
	s.Set("SHORT", "abc") // < 6 chars: not masked (avoids noisy false matches)

	out := string(s.Mask([]byte(`{"text":"the key is supersecretvalue here"}`)))
	if strings.Contains(out, "supersecretvalue") {
		t.Fatalf("value not masked: %s", out)
	}
	if !strings.Contains(out, "••••••") {
		t.Fatalf("no redaction marker: %s", out)
	}

	// Short values are left alone.
	shortOut := string(s.Mask([]byte("value abc value")))
	if !strings.Contains(shortOut, "abc") {
		t.Fatalf("short value should not be masked: %s", shortOut)
	}

	// Nothing to mask → same bytes, no allocation of a changed copy.
	clean := []byte("nothing sensitive here")
	if got := s.Mask(clean); string(got) != string(clean) {
		t.Fatalf("clean payload changed: %s", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
