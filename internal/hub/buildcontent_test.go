package hub

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUpload drops a file into uploadsDir/<session>/<name> and returns the name.
func writeUpload(t *testing.T, dir, session, name string, data []byte) string {
	t.Helper()
	sdir := filepath.Join(dir, session)
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestBuildContent_MultipleMixed(t *testing.T) {
	dir := t.TempDir()
	const sess = "s1"
	img := writeUpload(t, dir, sess, "a.png", []byte{0x89, 0x50, 0x4e, 0x47})
	txt := writeUpload(t, dir, sess, "notes.txt", []byte("hello from a file"))
	doc := writeUpload(t, dir, sess, "report.pdf", []byte("%PDF-1.4 binary"))

	h := &Hub{cfg: providers{uploadsDir: dir}}
	msg := &ClientMessage{
		Type:    "user",
		Content: "look at these",
		Attachments: []Attachment{
			{ID: "1", File: img, Name: "a.png", Type: "image/png"},
			{ID: "2", File: txt, Name: "notes.txt", Type: "text/plain"},
			{ID: "3", File: doc, Name: "report.pdf", Type: "application/pdf"},
		},
	}

	out := h.buildContent(sess, msg)
	blocks, ok := out.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected content-block array (image present), got %T", out)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (1 image + 1 text), got %d: %v", len(blocks), blocks)
	}
	if blocks[0]["type"] != "image" {
		t.Errorf("block[0] type = %v, want image", blocks[0]["type"])
	}
	src := blocks[0]["source"].(map[string]interface{})
	if want := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47}); src["data"] != want {
		t.Errorf("image data = %v, want %v", src["data"], want)
	}

	text, _ := blocks[1]["text"].(string)
	for _, want := range []string{"look at these", "notes.txt", "hello from a file", "report.pdf", "saved at"} {
		if !strings.Contains(text, want) {
			t.Errorf("trailing text block missing %q; got:\n%s", want, text)
		}
	}
}

func TestBuildContent_TextOnlyReturnsString(t *testing.T) {
	dir := t.TempDir()
	const sess = "s2"
	txt := writeUpload(t, dir, sess, "a.txt", []byte("file one"))
	txt2 := writeUpload(t, dir, sess, "b.txt", []byte("file two"))

	h := &Hub{cfg: providers{uploadsDir: dir}}
	msg := &ClientMessage{
		Type:    "user",
		Content: "two docs",
		Attachments: []Attachment{
			{File: txt, Name: "a.txt", Type: "text/plain"},
			{File: txt2, Name: "b.txt", Type: "text/plain"},
		},
	}

	out := h.buildContent(sess, msg)
	s, ok := out.(string)
	if !ok {
		t.Fatalf("expected string (no images), got %T", out)
	}
	for _, want := range []string{"two docs", "file one", "file two", "a.txt", "b.txt"} {
		if !strings.Contains(s, want) {
			t.Errorf("result missing %q; got:\n%s", want, s)
		}
	}
}

// Legacy single-attachment fields must still flow through allAttachments.
func TestBuildContent_LegacySingleField(t *testing.T) {
	dir := t.TempDir()
	const sess = "s3"
	txt := writeUpload(t, dir, sess, "legacy.txt", []byte("legacy body"))

	h := &Hub{cfg: providers{uploadsDir: dir}}
	msg := &ClientMessage{
		Type:           "user",
		Content:        "hi",
		AttachmentFile: txt,
		AttachmentName: "legacy.txt",
		AttachmentType: "text/plain",
	}

	out := h.buildContent(sess, msg)
	s, ok := out.(string)
	if !ok {
		t.Fatalf("expected string, got %T", out)
	}
	if !strings.Contains(s, "legacy body") || !strings.Contains(s, "hi") {
		t.Errorf("legacy path lost content; got:\n%s", s)
	}
}

// A file that fails to read is skipped, not fatal.
func TestBuildContent_MissingFileSkipped(t *testing.T) {
	dir := t.TempDir()
	const sess = "s4"
	txt := writeUpload(t, dir, sess, "real.txt", []byte("present"))

	h := &Hub{cfg: providers{uploadsDir: dir}}
	msg := &ClientMessage{
		Type:    "user",
		Content: "q",
		Attachments: []Attachment{
			{File: "ghost.txt", Name: "ghost.txt", Type: "text/plain"},
			{File: txt, Name: "real.txt", Type: "text/plain"},
		},
	}

	s, ok := h.buildContent(sess, msg).(string)
	if !ok {
		t.Fatalf("expected string, got %T", h.buildContent(sess, msg))
	}
	if !strings.Contains(s, "present") || strings.Contains(s, "ghost") {
		t.Errorf("missing file not skipped cleanly; got:\n%s", s)
	}
}
