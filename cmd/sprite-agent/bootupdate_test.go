package main

import "testing"

func TestShouldBootSelfUpdate(t *testing.T) {
	cases := []struct {
		disable string
		want    bool
	}{
		{"", true},        // default: converge on boot
		{"1", true},       // any non-disable value still converges
		{"0", false},      // opt-out pins the current binary
		{"false", false},  // opt-out (case-insensitive)
		{"FALSE", false},
	}
	for _, c := range cases {
		if got := shouldBootSelfUpdate(c.disable); got != c.want {
			t.Errorf("shouldBootSelfUpdate(disable=%q) = %v, want %v", c.disable, got, c.want)
		}
	}
}
