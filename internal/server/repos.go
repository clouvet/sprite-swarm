package server

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// repoInfo is one git repo checked out in a chat's workspace, as shown in the UI.
type repoInfo struct {
	Name   string `json:"name"`
	Remote string `json:"remote,omitempty"`
	Branch string `json:"branch,omitempty"`
	Dirty  bool   `json:"dirty"`
}

// serveSessionRepos mirrors the git repos in a chat's workspace (~/chats/<id>).
// Read-only: it reflects whatever the agent has cloned there — the UI shows this
// so you can see what repos a conversation is working with. No repos → empty list
// (so the UI panel simply stays hidden when you're just chatting).
func (s *Server) serveSessionRepos(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	base := filepath.Join(s.cfg.WorkDir, "chats", id)
	entries, err := os.ReadDir(base)
	if err != nil {
		writeJSON(w, []repoInfo{}) // no workspace yet
		return
	}
	repos := []repoInfo{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repoPath := filepath.Join(base, e.Name())
		// A git repo has a .git entry (dir for a normal clone, file for a worktree).
		if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
			continue
		}
		repos = append(repos, gitRepoInfo(repoPath, e.Name()))
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	writeJSON(w, repos)
}

func gitRepoInfo(path, name string) repoInfo {
	ri := repoInfo{Name: name}
	ri.Remote = strings.TrimSpace(gitOutput(path, "config", "--get", "remote.origin.url"))
	ri.Branch = strings.TrimSpace(gitOutput(path, "rev-parse", "--abbrev-ref", "HEAD"))
	ri.Dirty = strings.TrimSpace(gitOutput(path, "status", "--porcelain")) != ""
	return ri
}

// gitOutput runs a git command in path with a short timeout; "" on any error.
func gitOutput(path string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}
