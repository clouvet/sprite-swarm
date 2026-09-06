package main

import (
	"context"
	"log"
	"os"
	"os/exec"
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

// ensurePiInstalled installs the pi CLI globally on first boot if it's not present.
// The package is @earendil-works/pi-coding-agent; it requires Node/npm on the base
// image. Installs into a user-writable prefix so no root is needed.
func ensurePiInstalled(ctx context.Context) {
	if p := resolvePiBinary(); p != "pi" {
		return // an absolute path resolved → already installed
	}
	if _, err := exec.LookPath("pi"); err == nil {
		return
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		log.Printf("pi: npm not found on PATH — cannot install the pi CLI (the base image needs Node.js). " +
			"Install @earendil-works/pi-coding-agent manually, or bake it into the image.")
		return
	}
	log.Printf("pi: installing @earendil-works/pi-coding-agent (first boot; ~1-2 min)…")
	ic, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ic, npm, "install", "-g", "@earendil-works/pi-coding-agent")
	cmd.Env = append(os.Environ(), "npm_config_prefix=/home/sprite/.npm-global")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("pi: install failed: %v: %s", err, tailBytes(out, 600))
	} else {
		log.Printf("pi: installed the pi CLI")
	}
}

func tailBytes(b []byte, n int) string {
	if len(b) > n {
		return "…" + string(b[len(b)-n:])
	}
	return string(b)
}
