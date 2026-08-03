package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseHoneycombSecret(t *testing.T) {
	url, key, err := parseHoneycombSecret(`{"url":"https://mcp.honeycomb.io/mcp","key":"hcam_id:secret"}`)
	if err != nil || url != "https://mcp.honeycomb.io/mcp" || key != "hcam_id:secret" {
		t.Fatalf("parse -> url=%q key=%q err=%v", url, key, err)
	}
	for _, bad := range []string{
		`{"url":"https://mcp.honeycomb.io/mcp"}`, // no key
		`{"key":"hcam_id:secret"}`,               // no url
		`{}`, `not json`, ``,
	} {
		if _, _, err := parseHoneycombSecret(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestSetupHoneycombMCP(t *testing.T) {
	os.Unsetenv("HONEYCOMB_KEY")
	name, entry, err := setupHoneycombMCP(`{"url":"https://mcp.honeycomb.io/mcp","key":"hcam_id:secret"}`)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if name != "honeycomb" || entry["command"] != "npx" {
		t.Fatalf("name=%q command=%v", name, entry["command"])
	}
	args := entry["args"].([]string)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "mcp-remote") || !strings.Contains(joined, "https://mcp.honeycomb.io/mcp") {
		t.Errorf("args missing bridge/url: %v", args)
	}
	// Key is referenced via ${HONEYCOMB_KEY}, never embedded literally.
	if !strings.Contains(joined, "${HONEYCOMB_KEY}") {
		t.Errorf("expected ${HONEYCOMB_KEY} placeholder, got: %v", args)
	}
	if strings.Contains(joined, "hcam_id:secret") {
		t.Error("key must not appear literally in args")
	}
	if _, ok := entry["env"]; ok {
		t.Error("key must not be embedded in the mcp.json entry env")
	}
	if os.Getenv("HONEYCOMB_KEY") != "hcam_id:secret" {
		t.Error("HONEYCOMB_KEY not set in process env")
	}
	os.Unsetenv("HONEYCOMB_KEY")
}
