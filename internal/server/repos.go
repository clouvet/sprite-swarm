package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// contextInfo is everything added to a conversation, mirrored from disk: the git
// repos in its workspace, the files uploaded to it, the Discourse topics it pulled
// in (when configured), and the files the agent created in its workspace (its
// downloadable artifacts).
type contextInfo struct {
	Repos     []repoInfo     `json:"repos"`
	Files     []fileInfo     `json:"files"`
	Created   []fileInfo     `json:"created"`
	Discourse []discourseRef `json:"discourse"`
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
		Repos:     s.sessionRepos(id),
		Files:     s.sessionFiles(id),
		Created:   s.sessionCreatedFiles(id),
		Discourse: s.sessionDiscourse(id),
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

// sessionCreatedFiles lists the loose files the agent has written at the top level
// of the chat's workspace (~/chats/<id>) — the artifacts it produced — as
// downloadable entries, newest first. Directories (cloned repos, shown under
// Repos) and dotfiles are skipped, so the human can grab a report/export the agent
// made without copy-pasting it out of the chat. Capped so a busy workspace can't
// flood the panel.
func (s *Server) sessionCreatedFiles(id string) []fileInfo {
	base := filepath.Join(s.cfg.WorkDir, "chats", id)
	entries, err := os.ReadDir(base)
	if err != nil {
		return []fileInfo{}
	}
	type item struct {
		fi  fileInfo
		mod int64
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{
			fi: fileInfo{
				Name:  e.Name(),
				URL:   "/api/sessions/" + id + "/files/" + url.PathEscape(e.Name()),
				Image: isImageExt(filepath.Ext(e.Name())),
			},
			mod: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod > items[j].mod })
	files := []fileInfo{}
	for i, it := range items {
		if i >= 50 {
			break
		}
		files = append(files, it.fi)
	}
	return files
}

// serveCreatedFile downloads a file from the chat's workspace
// (GET /api/sessions/<id>/files/<name>), forcing attachment disposition so the
// browser saves it rather than rendering.
func (s *Server) serveCreatedFile(w http.ResponseWriter, r *http.Request, id, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id = sessionIDRe.ReplaceAllString(id, "")
	name = filepath.Base(name) // traversal defense: only a direct child
	if id == "" || name == "" || name == "." {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.WorkDir, "chats", id, name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	http.ServeFile(w, r, path)
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
