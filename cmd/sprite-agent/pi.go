package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
	"github.com/clouvet/sprite-swarm/internal/fleet"
	"github.com/clouvet/sprite-swarm/internal/gateway"
	"github.com/clouvet/sprite-swarm/internal/pi"
)

// setupPiRuntime prepares the experimental Pi backend (SPRITE_AGENT_RUNTIME=pi):
// resolve provider auth (a brain-uploaded key WINS, else a gateway connector, exactly
// like Claude's posture), export the key env / write ~/.pi/agent/models.json for
// connector-routed providers, ensure the `pi` CLI is installed, and default the model.
// No-op under the default Claude runtime.
func setupPiRuntime(ctx context.Context, fleetSvc *fleet.Service, cfg *config.Config) {
	provider := cfg.Provider
	if provider == "" {
		provider = "openai"
		cfg.Provider = provider
	}
	p, ok := pi.ProviderByName(provider)
	if !ok {
		log.Printf("pi: unknown provider %q (supported: openai, anthropic, google); defaulting to openai", provider)
		p, _ = pi.ProviderByName("openai")
		cfg.Provider = p.Name
	}

	getSecret := func(c context.Context, name string) string {
		if fleetSvc == nil {
			return ""
		}
		return fleetSvc.GetSecret(c, name)
	}

	// Subscription auth WINS (like Claude's subscription token over the connector): if
	// the operator uploaded their Pi auth.json (from `pi` + `/login` on their machine,
	// stored in the brain as "pi-auth-json"), materialize it so Pi uses the ChatGPT/
	// Claude subscription instead of a metered API key. Pi prefers subscription creds
	// when present.
	loadPiSubscriptionAuth(ctx, getSecret)

	// Resolve every known provider (so a connector for a secondary one still works if
	// present), and require the PRIMARY (selected) one to be available.
	var auths []pi.Auth
	for _, prov := range pi.Providers {
		auths = append(auths, pi.Resolve(ctx, prov, getSecret, gateway.ConnectorBase))
	}
	primary := pi.Resolve(ctx, p, getSecret, gateway.ConnectorBase)
	switch {
	case !primary.Available:
		log.Printf("pi: provider %q has NO auth — upload a brain key %q or add a %q gateway connector; the runtime will fail to answer",
			p.Name, p.SecretName, p.Name)
	case primary.ViaKey:
		log.Printf("pi: provider %q authed via brain-uploaded key (wins over connector)", p.Name)
	default:
		log.Printf("pi: provider %q authed via gateway connector (token-free, by sprite identity)", p.Name)
	}

	// Export the resolved keys so the pi subprocess inherits them (kept out of any
	// on-disk config, like the Claude token). Connector providers need no key.
	for _, a := range auths {
		for k, v := range a.Env {
			_ = os.Setenv(k, v)
		}
	}

	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/sprite"
	}
	if path, err := pi.WriteModelsJSON(home, auths); err != nil {
		log.Printf("pi: write models.json: %v", err)
	} else if path != "" {
		log.Printf("pi: wrote %s (connector-routed providers)", path)
	}

	if cfg.Model == "" {
		cfg.Model = p.Default
	}
	ensurePiInstalled(ctx)
	log.Printf("pi: runtime ready (provider=%s model=%s)", cfg.Provider, cfg.Model)
}

// loadPiSubscriptionAuth writes a brain-stored Pi auth.json (subscription logins,
// e.g. ChatGPT Plus/Pro or Claude Pro/Max) to ~/.pi/agent/auth.json (0600) so a
// headless sprite can use the subscription — you can't run the interactive `/login`
// browser flow on a sprite, so you log in on your own machine and upload the
// resulting auth.json (secret "pi-auth-json"). No secret → no-op (fall back to the
// API key / connector). Pi auto-refreshes the tokens from here.
func loadPiSubscriptionAuth(ctx context.Context, getSecret func(context.Context, string) string) {
	blob := strings.TrimSpace(getSecret(ctx, "pi-auth-json"))
	if blob == "" {
		return
	}
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/sprite"
	}
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("pi: subscription auth dir: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(blob), 0o600); err != nil {
		log.Printf("pi: write subscription auth.json: %v", err)
		return
	}
	log.Printf("pi: loaded subscription auth from brain (wins over metered API keys)")
}

// piMinVersion is the lowest pi we accept: the first release whose bundled model
// catalog carries the current OpenAI (GPT-5.x / GPT-6 Astra) and Claude ids. A baked
// image ships an older pi, so we upgrade it — otherwise the picker offers models pi
// treats as unknown "custom" ids and the turn silently hangs.
const piMinVersion = "0.85.1"

// ensurePiInstalled makes a pi CLI of at least piMinVersion available. If the resolved
// pi is missing or too old (e.g. the base image's baked copy), it installs the latest
// @earendil-works/pi-coding-agent into a user-writable prefix that resolvePiBinary
// prefers — no root needed. Requires Node/npm on the base image.
func ensurePiInstalled(ctx context.Context) {
	if v := piVersion(resolvePiBinary()); v != "" && !semverLessStr(v, piMinVersion) {
		return // a current-enough pi is already resolvable, wherever it lives
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		log.Printf("pi: npm not found on PATH — cannot install the pi CLI (the base image needs Node.js). " +
			"Install @earendil-works/pi-coding-agent manually, or bake it into the image.")
		return
	}
	log.Printf("pi: installing/upgrading @earendil-works/pi-coding-agent (need >= %s; ~1-2 min)…", piMinVersion)
	ic, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ic, npm, "install", "-g", "@earendil-works/pi-coding-agent@latest")
	// npm_config_prefix works in the service context (unlike an nvm-sourced interactive
	// shell); it lands in /home/sprite/.npm-global/bin/pi, which resolvePiBinary checks first.
	cmd.Env = append(os.Environ(), "npm_config_prefix=/home/sprite/.npm-global")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("pi: install failed: %v: %s", err, tailBytes(out, 600))
	} else {
		log.Printf("pi: installed pi %s", piVersion(resolvePiBinary()))
	}
}

// piVersion returns the `pi --version` string (e.g. "0.85.1"), or "" if the binary
// can't be run. bin may be an absolute path or "pi" (unresolved).
func piVersion(bin string) string {
	if bin == "" {
		return ""
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// semverLessStr reports whether version a < b for plain "X.Y.Z" strings. A version
// that doesn't parse sorts as older (so an unrecognized pi is treated as too old and
// gets upgraded rather than trusted).
func semverLessStr(a, b string) bool {
	pa, oka := parse3(a)
	pb, okb := parse3(b)
	if !oka {
		return true
	}
	if !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

// parse3 parses the leading "X.Y.Z" of a version string into three ints, ignoring any
// pre-release/build suffix. ok=false if the first three dot-parts aren't all numeric.
func parse3(v string) ([3]int, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	if i := strings.IndexAny(v, "-+ \t"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func tailBytes(b []byte, n int) string {
	if len(b) > n {
		return "…" + string(b[len(b)-n:])
	}
	return string(b)
}
