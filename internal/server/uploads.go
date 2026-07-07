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

// allowedDocExt maps accepted non-image file extensions to a media type.
var allowedDocExt = map[string]string{
	".txt":  "text/plain",
	".md":   "text/markdown",
	".csv":  "text/csv",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// serveUpload accepts a multipart file (POST /api/upload?session=<id>) — an image
// or one of the allowed document types — stores it under uploadsDir/<session>/, and
// returns {id, filename, name, mediaType, kind, url}. The chat message references it
// by filename; the hub feeds it to Claude (image block, inlined text, or a path).
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
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB
		http.Error(w, "bad multipart form", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file field required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 32<<20))
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}

	origName := filepath.Base(header.Filename)
	ext := strings.ToLower(filepath.Ext(origName))
	media := header.Header.Get("Content-Type")

	var kind, storedExt string
	switch {
	case strings.HasPrefix(media, "image/"), strings.HasPrefix(http.DetectContentType(data), "image/"):
		kind = "image"
		if !strings.HasPrefix(media, "image/") {
			media = http.DetectContentType(data)
		}
		storedExt = extForMedia(media)
	case allowedDocExt[ext] != "":
		kind = "file"
		media = allowedDocExt[ext]
		storedExt = ext
	default:
		http.Error(w, "unsupported file type: "+ext, http.StatusBadRequest)
		return
	}

	id := newUUID()
	filename := id + storedExt
	dir := filepath.Join(s.uploadsDir(), session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if origName == "" || origName == "." {
		origName = filename
	}
	// Sidecar the original name so the context bar can mirror a readable name
	// (the stored file is <uuid><ext>; the original name lives only here on disk).
	_ = os.WriteFile(filepath.Join(dir, filename+".name"), []byte(origName), 0o644)
	writeJSON(w, map[string]string{
		"id": id, "filename": filename, "name": origName, "mediaType": media, "kind": kind,
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
