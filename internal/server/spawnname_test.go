package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/spawn"
)

func TestWorkerSlug(t *testing.T) {
	cases := map[string]string{
		"posthog integration":       "posthog-integration",
		"PostHog Integration":       "posthog-integration",
		"  help with the PostHog  ": "help-with-the-posthog",
		"Fix bug #1234!":            "fix-bug-1234",
		"a/b\\c":                    "a-b-c",
		"under_scores_here":         "under-scores-here",
		"":                          "",
		"!!!":                       "",
		"---":                       "",
		"café résumé":               "caf-r-sum", // non-ASCII folded to hyphens
	}
	for in, want := range cases {
		if got := workerSlug(in); got != want {
			t.Errorf("workerSlug(%q) = %q, want %q", in, got, want)
		}
	}
	// Length cap (32) with no trailing hyphen.
	long := workerSlug(strings.Repeat("ab ", 40))
	if len(long) > maxWorkerSlug || strings.HasSuffix(long, "-") {
		t.Errorf("long slug not capped/trimmed: %q (len %d)", long, len(long))
	}
}

// fakeSpawner records the Spawn request and can simulate an existing name.
type fakeSpawner struct {
	taken       map[string]bool
	lastReq     spawn.Request
	destroyed   []string
	updatedName string
	updatedReq  spawn.DeployRequest
}

func (f *fakeSpawner) Available() bool { return true }
func (f *fakeSpawner) Spawn(_ context.Context, req spawn.Request) (spawn.Result, error) {
	f.lastReq = req
	name := req.Name
	if name == "" {
		name = req.NamePrefix + "deadbeef"
	}
	return spawn.Result{ID: name, Name: name}, nil
}
func (f *fakeSpawner) Destroy(_ context.Context, name string) error {
	f.destroyed = append(f.destroyed, name)
	return nil
}
func (f *fakeSpawner) Exists(_ context.Context, name string) (bool, error) {
	return f.taken[name], nil
}
func (f *fakeSpawner) SetEnv(_ context.Context, _ string, _ map[string]string) error { return nil }
func (f *fakeSpawner) DeployApp(context.Context, spawn.DeployRequest) (spawn.Result, error) {
	return spawn.Result{}, nil
}
func (f *fakeSpawner) UpdateApp(_ context.Context, name string, req spawn.DeployRequest) (spawn.Result, error) {
	f.updatedName = name
	f.updatedReq = req
	return spawn.Result{ID: name, Name: name}, nil
}

func TestServeSpawnNaming(t *testing.T) {
	// A label yields a descriptive wk-<slug> name passed to the spawner.
	f := &fakeSpawner{taken: map[string]bool{}}
	s := &Server{spawner: f}
	r := httptest.NewRequest("POST", "/api/fleet/spawn", strings.NewReader(`{"label":"PostHog Integration"}`))
	s.serveSpawn(httptest.NewRecorder(), r)
	if f.lastReq.Name != "wk-posthog-integration" {
		t.Errorf("label spawn name = %q, want wk-posthog-integration", f.lastReq.Name)
	}

	// No label -> fall back to the random wk- prefix (empty explicit name).
	f2 := &fakeSpawner{taken: map[string]bool{}}
	s2 := &Server{spawner: f2}
	r2 := httptest.NewRequest("POST", "/api/fleet/spawn", strings.NewReader(`{}`))
	s2.serveSpawn(httptest.NewRecorder(), r2)
	if f2.lastReq.Name != "" || f2.lastReq.NamePrefix != "wk-" {
		t.Errorf("no-label spawn = name %q prefix %q, want name \"\" prefix wk-", f2.lastReq.Name, f2.lastReq.NamePrefix)
	}
}

func TestUniqueWorkerName(t *testing.T) {
	s := &Server{spawner: &fakeSpawner{taken: map[string]bool{"wk-posthog": true}}}
	if got := s.uniqueWorkerName("wk-newthing"); got != "wk-newthing" {
		t.Errorf("free name changed: %q", got)
	}
	got := s.uniqueWorkerName("wk-posthog")
	if !strings.HasPrefix(got, "wk-posthog-") || got == "wk-posthog" {
		t.Errorf("collision not disambiguated: %q", got)
	}
}
