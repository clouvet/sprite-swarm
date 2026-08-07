package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func msg(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMessageBody(t *testing.T) {
	// Plain text.
	if got := messageBody(msg(t, `{"text":"hello world"}`)); got != "hello world" {
		t.Errorf("plain text: %q", got)
	}

	// Bot alert: empty text, content in attachments (fallback/title/fields).
	m := msg(t, `{"text":"","attachments":[{"title":"DB Load alert","fallback":"[FIRING:1] DB Load alert dev","fields":[{"title":"Severity","value":"critical"}]}]}`)
	got := messageBody(m)
	for _, want := range []string{"DB Load alert", "[FIRING:1]", "Severity: critical"} {
		if !strings.Contains(got, want) {
			t.Errorf("attachment body missing %q in %q", want, got)
		}
	}

	// Block Kit section text when no text/attachments.
	if got := messageBody(msg(t, `{"blocks":[{"type":"section","text":{"type":"mrkdwn","text":"deploy finished"}}]}`)); !strings.Contains(got, "deploy finished") {
		t.Errorf("blocks body: %q", got)
	}

	// Uploaded file is listed with name + mimetype.
	got = messageBody(msg(t, `{"text":"see this","files":[{"name":"report.pdf","mimetype":"application/pdf","permalink":"https://x/f"}]}`))
	if !strings.Contains(got, "see this") || !strings.Contains(got, "report.pdf") || !strings.Contains(got, "application/pdf") {
		t.Errorf("file listing: %q", got)
	}
}

func TestBlocksTextDedup(t *testing.T) {
	// rich_text nesting shouldn't repeat the same run.
	m := msg(t, `{"blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"hi there"}]}]}]}`)
	if got := messageBody(m); strings.Count(got, "hi there") != 1 {
		t.Errorf("expected 'hi there' once, got %q", got)
	}
}
