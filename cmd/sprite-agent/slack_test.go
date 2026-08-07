package main

import (
	"testing"
	"time"
)

func TestIsChannelID(t *testing.T) {
	yes := []string{"C504D8602", "G06ABC1234", "D06DC6FKRDX"}
	no := []string{"dev", "#dev", "general", "c504d8602", "C123"}
	for _, s := range yes {
		if !isChannelID(s) {
			t.Errorf("%q should be a channel id", s)
		}
	}
	for _, s := range no {
		if isChannelID(s) {
			t.Errorf("%q should NOT be a channel id", s)
		}
	}
}

func TestUnixFromRange(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if got := unixFromRange(now, 0, 0); got != "" {
		t.Errorf("no range should be empty, got %q", got)
	}
	if got := unixFromRange(now, 1, 0); got != "996400" { // 1h = 3600s back
		t.Errorf("1h: got %q want 996400", got)
	}
	if got := unixFromRange(now, 0, 2); got != "827200" { // 2d = 172800s back
		t.Errorf("2d: got %q want 827200", got)
	}
}

func TestArgHelpers(t *testing.T) {
	a := map[string]any{"s": "  hi ", "n": float64(7), "empty": ""}
	if argStr(a, "s") != "hi" {
		t.Errorf("argStr trim failed: %q", argStr(a, "s"))
	}
	if argStr(a, "missing") != "" {
		t.Errorf("argStr missing should be empty")
	}
	if argInt(a, "n") != 7 {
		t.Errorf("argInt float64 failed: %d", argInt(a, "n"))
	}
	if argInt(a, "missing") != 0 {
		t.Errorf("argInt missing should be 0")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "x", "y") != "x" {
		t.Error("firstNonEmpty should return first non-empty")
	}
	if firstNonEmpty("", "") != "" {
		t.Error("all empty should be empty")
	}
}
