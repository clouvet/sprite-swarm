package main

import "testing"

func TestDecideClaudeAuth(t *testing.T) {
	cases := []struct {
		name                                    string
		hasToken, force, hasBaseURL, hasLogin   bool
		wantSubscription, wantTryConnector, none bool
	}{
		{name: "token → subscription", hasToken: true, wantSubscription: true},
		{name: "token + login → subscription", hasToken: true, hasLogin: true, wantSubscription: true},
		{name: "token but forced → connector", hasToken: true, force: true, wantTryConnector: true},
		{name: "no token → connector", wantTryConnector: true},
		{name: "no token, has login → leave as-is", hasLogin: true, none: true},
		{name: "no token, explicit base url → leave as-is", hasBaseURL: true, none: true},
		{name: "forced, has login → connector (override)", force: true, hasLogin: true, wantTryConnector: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := decideClaudeAuth(c.hasToken, c.force, c.hasBaseURL, c.hasLogin)
			if p.subscription != c.wantSubscription {
				t.Errorf("subscription = %v, want %v", p.subscription, c.wantSubscription)
			}
			if p.tryConnector != c.wantTryConnector {
				t.Errorf("tryConnector = %v, want %v", p.tryConnector, c.wantTryConnector)
			}
			leaveAsIs := !p.subscription && !p.tryConnector
			if c.none && !leaveAsIs {
				t.Errorf("expected leave-as-is, got subscription=%v tryConnector=%v", p.subscription, p.tryConnector)
			}
		})
	}
}
