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

// contextInfo is everything added to a conversation, mirrored from disk: the git
// repos in its workspace and the files uploaded to it.
type contextInfo struct {
	Repos []repoInfo `json:"repos"`
	Files []fileInfo `json:"files"`
}

// repoInfo is one git repo checked out in a chat's workspace.
type repoInfo struct {
	Name   string `json:"name"`
	Remote string `json:"remote,omitempty"`
	Branch string `json:"branch,omitempty"`
	Dirty  bool   `json:"dirty"`
}

// fileInfo is one file uploaded to a chat.
type fileInfo struct {
	Name  string `json:"name"`  // original (readable) name
	URL   string `json:"url"`   // where the UI can fetch it
	Image bool   `json:"image"` // hint for the chip icon
}

// serveSessionContext mirrors what a conversation is working with: git repos in
// its workspace (~/chats/<id>) plus files uploaded to it. Read-only — it reflects
// whatever is on disk, so anything the agent clones or the user uploads shows up,
// and the UI panel stays hidden when there's nothing (just-chatting is unaffected).
func (s *Server) serveSessionContext(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id = sessionIDRe.ReplaceAllString(id, "") // defend the path joins below
	writeJSON(w, contextInfo{
		Repos: s.sessionRepos(id),
		Files: s.sessionFiles(id),
	})
}

// sessionRepos lists the git repos directly under the chat's workspace.
func (s *Server) sessionRepos(id string) []repoInfo {
	base := filepath.Join(s.cfg.WorkDir, "chats", id)
	entries, err := os.ReadDir(base)
	if err != nil {
		return []repoInfo{}
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
	return repos
}

// sessionFiles lists the files uploaded to the chat, using the readable name from
// each file's ".name" sidecar (falling back to the stored name).
func (s *Server) sessionFiles(id string) []fileInfo {
	dir := filepath.Join(s.uploadsDir(), id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []fileInfo{}
	}
	files := []fileInfo{}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".name") {
			continue
		}
		stored := e.Name()
		name := stored
		if b, err := os.ReadFile(filepath.Join(dir, stored+".name")); err == nil {
			if n := strings.TrimSpace(string(b)); n != "" {
				name = n
			}
		}
		files = append(files, fileInfo{
			Name:  name,
			URL:   "/api/uploads/" + id + "/" + stored,
			Image: isImageExt(filepath.Ext(stored)),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files
}

func isImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".img":
		return true
	}
	return false
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
