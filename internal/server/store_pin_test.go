package server

import (
	"path/filepath"
	"testing"
)

func TestSetPinnedPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	s := newMetaStore(path)
	s.EnsureNamed("s1", "First")
	s.EnsureNamed("s2", "Second")

	s.SetPinned("s1", true)
	// Pinned state is set.
	found := false
	for _, m := range s.List() {
		if m.ID == "s1" {
			found = true
			if !m.Pinned {
				t.Fatalf("s1 should be pinned")
			}
		}
		if m.ID == "s2" && m.Pinned {
			t.Fatalf("s2 should not be pinned")
		}
	}
	if !found {
		t.Fatalf("s1 missing from list")
	}

	// Reload from disk: pin survives.
	s2 := newMetaStore(path)
	for _, m := range s2.List() {
		if m.ID == "s1" && !m.Pinned {
			t.Fatalf("pin did not persist across reload")
		}
	}

	// Unpin.
	s2.SetPinned("s1", false)
	for _, m := range s2.List() {
		if m.ID == "s1" && m.Pinned {
			t.Fatalf("s1 should be unpinned")
		}
	}
}
