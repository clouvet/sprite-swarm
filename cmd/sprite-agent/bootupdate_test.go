package main

import "testing"

func TestShouldBootSelfUpdate(t *testing.T) {
	cases := []struct {
		role, disable string
		want          bool
	}{
		{"worker", "", true},  // a worker converges on boot
		{"", "", true},        // empty role = worker default
		{"home", "", false},   // home never self-adopts the staged binary
		{"HOME", "", false},   // case-insensitive
		{"worker", "0", false}, // opt-out
		{"worker", "false", false},
		{"", "0", false},
		{"home", "0", false},
	}
	for _, c := range cases {
		if got := shouldBootSelfUpdate(c.role, c.disable); got != c.want {
			t.Errorf("shouldBootSelfUpdate(role=%q, disable=%q) = %v, want %v", c.role, c.disable, got, c.want)
		}
	}
}
