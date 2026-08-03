package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseSentrySecret(t *testing.T) {
	// host + access_token, and the `token` alias.
	for _, s := range []string{
		`{"host":"flyio.sentry.io","access_token":"sntryu_abc"}`,
		`{"host":"flyio.sentry.io","token":"sntryu_abc"}`,
	} {
		host, tok, err := parseSentrySecret(s)
		if err != nil || host != "flyio.sentry.io" || tok != "sntryu_abc" {
			t.Fatalf("parse %s -> host=%q tok=%q err=%v", s, host, tok, err)
		}
	}
	for _, bad := range []string{
		`{"host":"flyio.sentry.io"}`, // no token
		`{"access_token":"x"}`,       // no host
		`{}`, `not json`, ``,
	} {
		if _, _, err := parseSentrySecret(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestSetupSentryMCP(t *testing.T) {
	os.Unsetenv("SENTRY_ACCESS_TOKEN")
	name, entry, err := setupSentryMCP(`{"host":"flyio.sentry.io","access_token":"sntryu_secret"}`)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if name != "sentry" || entry["command"] != "npx" {
		t.Fatalf("name=%q command=%v", name, entry["command"])
	}
	args := strings.Join(entry["args"].([]string), " ")
	if !strings.Contains(args, "@sentry/mcp-server") || !strings.Contains(args, "--host=flyio.sentry.io") {
		t.Errorf("args missing server/host: %v", entry["args"])
	}
	// Token goes to the process env, NOT into the mcp.json entry.
	if _, ok := entry["env"]; ok {
		t.Error("token must not be embedded in the mcp.json entry")
	}
	if os.Getenv("SENTRY_ACCESS_TOKEN") != "sntryu_secret" {
		t.Errorf("SENTRY_ACCESS_TOKEN not set in process env")
	}
	os.Unsetenv("SENTRY_ACCESS_TOKEN")
}
