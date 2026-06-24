package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// uploadsDir is where attached images are stored, per session, under the workdir.
func (s *Server) uploadsDir() string {
	return filepath.Join(s.cfg.WorkDir, ".sprite-agent", "uploads")
}

var sessionIDRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// serveConfig exposes this sprite's identity so the UI can title itself
// "sprite agent #<id>" (item #24) without guessing from the URL.
func (s *Server) serveConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"agentID": s.cfg.AgentID, "url": s.cfg.PublicURL})
}

// serveUpload accepts a multipart image (POST /api/upload?session=<id>), stores it
// under uploadsDir/<session>/<id>.<ext>, and returns {id, filename, mediaType, url}.
// The chat message then references it by filename; the hub base64-encodes it for Claude.
func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	session := sessionIDRe.ReplaceAllString(r.URL.Query().Get("session"), "")
	if session == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil { // 16MB
		http.Error(w, "bad multipart form", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file field required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 16<<20))
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	media := header.Header.Get("Content-Type")
	if !strings.HasPrefix(media, "image/") {
		media = http.DetectContentType(data)
	}
	if !strings.HasPrefix(media, "image/") {
		http.Error(w, "not an image", http.StatusBadRequest)
		return
	}
	ext := extForMedia(media)
	id := newUUID()
	filename := id + ext

	dir := filepath.Join(s.uploadsDir(), session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{
		"id": id, "filename": filename, "mediaType": media,
		"url": "/api/uploads/" + session + "/" + filename,
	})
}

// serveUploadFile serves a stored upload (GET /api/uploads/<session>/<file>).
func (s *Server) serveUploadFile(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/uploads/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	session := sessionIDRe.ReplaceAllString(parts[0], "")
	name := filepath.Base(parts[1]) // defends against traversal
	if session == "" || name == "" || name == "." {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.uploadsDir(), session, name))
}

func extForMedia(media string) string {
	switch media {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".img"
	}
}
