package spawn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
	"github.com/clouvet/sprite-swarm/internal/gateway"
)

// apiSpawner creates sprites via the sprites REST API (POST /v1/sprites, Bearer
// auth with the full token). The endpoint, auth, and create body (name required;
// env + labels accepted) were verified against the live cl-sprites API. The base
// URL is overridable via SPRITE_API_BASE.
//
// What this does NOT do: provision the sprite-agent artifact onto the new sprite.
// A bare create yields a base-environment sprite; making it boot sprite-agent and
// register into the brain needs a follow-up provisioning step (push/build the
// binary + run it as a service).
type apiSpawner struct {
	cfg          config.Config
	token        tokenParts
	base         string
	client       *http.Client
	newID        func() string // short unique suffix for synthesized sprite names
	provision    bool          // provision sprite-agent onto the new sprite after create
	pollInterval time.Duration // warm-poll interval (overridable in tests)
}

// artifactTTL is how long the presigned download URL handed to a new sprite is
// valid — long enough to boot, short enough to not linger.
const artifactTTL = 30 * time.Minute

// tokenParts is the SPRITE_API_TOKEN shape: org-slug/org-id/token-id/token-value.
type tokenParts struct {
	OrgSlug    string
	OrgID      string
	TokenID    string
	TokenValue string
}

func parseToken(raw string) (tokenParts, error) {
	parts := strings.Split(strings.TrimSpace(raw), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return tokenParts{}, fmt.Errorf("spawn: malformed SPRITE_API_TOKEN (want org-slug/org-id/token-id/token-value)")
	}
	return tokenParts{OrgSlug: parts[0], OrgID: parts[1], TokenID: parts[2], TokenValue: parts[3]}, nil
}

func newAPISpawner(cfg config.Config) Spawner {
	var base string
	var tp tokenParts
	if cfg.SpriteAPIToken != "" {
		// Token mode: call the Sprites API directly with a Bearer token.
		t, err := parseToken(cfg.SpriteAPIToken)
		if err != nil {
			// A malformed token means we cannot spawn; degrade to the stub so the
			// failure is explicit rather than a confusing runtime panic.
			return notConfigured{}
		}
		tp = t
		base = os.Getenv("SPRITE_API_BASE")
		if base == "" {
			base = "https://api.sprites.dev"
		}
	} else {
		// Connector mode: route through a custom_api gateway fronting the Sprites
		// API. No token on the sprite — the gateway injects it by sprite identity,
		// so do() sends no Authorization header (see below).
		base = cfg.SpriteAPIGateway
	}
	return &apiSpawner{
		cfg:          cfg,
		token:        tp,
		base:         strings.TrimRight(base, "/"),
		client:       &http.Client{Timeout: 90 * time.Second},
		newID:        randomSuffix,
		pollInterval: 2 * time.Second,
		// Provision by default; SPRITE_AGENT_SPAWN_PROVISION=0 yields a bare create.
		provision: cfg.Brain.Enabled() && os.Getenv("SPRITE_AGENT_SPAWN_PROVISION") != "0",
	}
}

// randomSuffix returns 8 hex chars for synthesizing unique sprite names.
func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

func (a *apiSpawner) Available() bool { return true }

// createSpriteRequest is the JSON body for POST /v1/sprites. Verified against
// the live API (cl-sprites): name is required; env + labels are accepted. The
// sprite name carries the restricted-token prefix, so name_prefix/org_id are not
// part of the body.
type createSpriteRequest struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// spriteName resolves the explicit name, or synthesizes <prefix><id> using the
// new agent id so the restricted-token name_prefix cap is honored.
func spriteName(req Request, newID string) string {
	if req.Name != "" {
		return req.Name
	}
	return req.NamePrefix + newID
}

// buildCreateRequest assembles the create-sprite payload, including the
// bootstrap env that points the new sprite at the same brain + artifact. The
// sprite's name is also its SPRITE_AGENT_ID so it registers under that id.
func (a *apiSpawner) buildCreateRequest(req Request) createSpriteRequest {
	role := req.Role
	if role == "" {
		role = "worker"
	}
	labels := map[string]string{"fleet": "sprite-agent", "role": role}
	for k, v := range req.Labels {
		labels[k] = v
	}
	name := spriteName(req, a.newID())
	return createSpriteRequest{
		Name:   name,
		Labels: labels,
		Env:    BootstrapEnv(a.cfg, name, role),
	}
}

// serviceSpec is the body for PUT /v1/sprites/<name>/services/<svc> (verified
// against the live API): cmd + args, working dir, env, and an http_port that the
// proxy routes to and auto-starts/keeps-alive.
type serviceSpec struct {
	Cmd      string            `json:"cmd"`
	Args     []string          `json:"args"`
	Dir      string            `json:"dir,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	HTTPPort int               `json:"http_port,omitempty"`
}

// Spawn creates a sprite and (unless provisioning is disabled) provisions
// sprite-agent onto it so it boots and self-registers into the brain.
func (a *apiSpawner) Spawn(ctx context.Context, req Request) (Result, error) {
	role := req.Role
	if role == "" {
		role = "worker"
	}
	cr := a.buildCreateRequest(req)
	res, err := a.createSprite(ctx, cr)
	if err != nil {
		return Result{}, err
	}
	if a.provision {
		// Hand the worker its own public URL so it advertises it in the roster
		// (the human attaches to it, P2.4).
		env := cr.Env
		if res.URL != "" {
			env["SPRITE_AGENT_URL"] = res.URL
		}
		// The sprite is created (fast). Provisioning — warm → PUT service → confirm —
		// takes 60-150s and would blow past the proxy's request timeout (a 502). Do it
		// in the BACKGROUND with its own context (the request ctx dies when the handler
		// returns); the worker registers into the brain on boot, so the roster the UI
		// polls reflects it. Return the created sprite immediately.
		name := res.Name
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			url, err := a.stageSelf(bg)
			if err != nil {
				log.Printf("spawn: staging artifact for %s failed: %v", name, err)
				return
			}
			if err := a.provisionAgent(bg, name, env, url); err != nil {
				log.Printf("spawn: background provisioning of %s failed: %v", name, err)
			} else {
				log.Printf("spawn: %s provisioned (registering into the brain)", name)
			}
		}()
	}
	return res, nil
}

// appServiceSpec is the service body that fetches the app tarball, extracts it,
// and runs the start command. Shared by deploy (new sprite) and update (existing
// sprite). The app dir is wiped first so an update fully replaces the old files.
func appServiceSpec(req DeployRequest) ([]byte, error) {
	boot := "set -e; rm -rf /home/sprite/app; mkdir -p /home/sprite/app; " +
		"curl -fsSL " + shQuote(req.ArtifactURL) + " -o /tmp/app.tgz; " +
		"tar xzf /tmp/app.tgz -C /home/sprite/app; cd /home/sprite/app; " +
		// Tolerate a tarball that wraps everything in one top-level dir (a common
		// `tar czf x.tgz mydir` mistake) — descend into it so the app root is right.
		"if [ \"$(ls -1A | wc -l)\" -eq 1 ] && [ -d \"$(ls -1A)\" ]; then cd \"$(ls -1A)\"; fi; " +
		"exec sh -c " + shQuote(req.Run)
	return json.Marshal(serviceSpec{
		Cmd: "/bin/sh", Args: []string{"-c", boot}, Dir: "/home/sprite", HTTPPort: req.HTTPPort,
	})
}

// installAppService warms the sprite and (re)installs the app service in the
// background — the create and update paths share it. action labels the log line
// ("deploy" / "update"). Runs async because warm→PUT→confirm takes 60-150s, past
// the proxy's request timeout; the caller has already returned the sprite.
func (a *apiSpawner) installAppService(name string, body []byte, port int, action string) {
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			if err := a.warmSprite(bg, name); err != nil {
				lastErr = err
				continue
			}
			if err := a.putService(bg, name, body); err != nil {
				lastErr = err
				continue
			}
			if a.serviceExists(bg, name) {
				log.Printf("%s: %s app service installed (port %d)", action, name, port)
				return
			}
			lastErr = fmt.Errorf("service did not persist")
		}
		log.Printf("%s: %s app provisioning failed: %v", action, name, lastErr)
	}()
}

// DeployApp hosts a user app on a dedicated BARE sprite (no agent). It creates the
// sprite (labeled role=app so it's identifiable), then in the background installs
// a service that fetches + runs the app tarball on req.HTTPPort — so the sprite's
// public URL serves the app (behind org auth). Returns the created sprite
// immediately. Change it later with UpdateApp; tear it down with Destroy.
func (a *apiSpawner) DeployApp(ctx context.Context, req DeployRequest) (Result, error) {
	if req.ArtifactURL == "" || req.Run == "" || req.HTTPPort == 0 {
		return Result{}, fmt.Errorf("deploy: artifact_url, run, and http_port are required")
	}
	prefix := req.NamePrefix
	if prefix == "" {
		prefix = "app-"
	}
	body, err := appServiceSpec(req)
	if err != nil {
		return Result{}, err
	}
	res, err := a.createSprite(ctx, createSpriteRequest{
		Name:   prefix + a.newID(),
		Labels: map[string]string{"fleet": "sprite-agent", "role": "app"},
	})
	if err != nil {
		return Result{}, err
	}
	a.installAppService(res.Name, body, req.HTTPPort, "deploy")
	return res, nil
}

// UpdateApp re-installs the app service on an EXISTING app sprite (new tarball,
// start command, or port), so an app can be changed in place without a new sprite
// or URL. The sprite must already exist.
func (a *apiSpawner) UpdateApp(ctx context.Context, name string, req DeployRequest) (Result, error) {
	if name == "" {
		return Result{}, fmt.Errorf("update: app sprite name is required")
	}
	if req.ArtifactURL == "" || req.Run == "" || req.HTTPPort == 0 {
		return Result{}, fmt.Errorf("update: artifact_url, run, and http_port are required")
	}
	exists, err := a.Exists(ctx, name)
	if err != nil {
		return Result{}, err
	}
	if !exists {
		return Result{}, fmt.Errorf("update: no such app sprite %q (deploy it first)", name)
	}
	body, err := appServiceSpec(req)
	if err != nil {
		return Result{}, err
	}
	a.installAppService(name, body, req.HTTPPort, "update")
	return Result{ID: name, Name: name}, nil
}

// shQuote single-quotes a string for safe embedding in a /bin/sh -c command.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (a *apiSpawner) createSprite(ctx context.Context, cr createSpriteRequest) (Result, error) {
	body, err := json.Marshal(cr)
	if err != nil {
		return Result{}, err
	}
	resp, err := a.do(ctx, http.MethodPost, a.base+"/v1/sprites", body)
	if err != nil {
		return Result{}, fmt.Errorf("spawn: create sprite: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("spawn: sprites API %d: %s", resp.StatusCode, string(data))
	}
	var out Result
	if err := json.Unmarshal(data, &out); err != nil {
		return Result{}, fmt.Errorf("spawn: decode response: %w", err)
	}
	return out, nil
}

// provisionAgent stages this binary in the brain, presigns a download URL, and
// installs a service on the new sprite that fetches and runs it with the
// bootstrap env (so it registers into the same brain). exec/fs are control-ws
// only; this uses the plain-REST services API + a presigned URL instead.
//
// A freshly-created sprite is "cold": a service PUT to it returns 200 but does
// NOT persist. So we warm it first and confirm the service stuck (retrying once),
// which is the difference between a worker that registers and one that never boots.
// stageSelf uploads this running binary to the brain and returns a URL the new
// sprite fetches it from on boot — via the s3 connector (token-free) when available,
// else a presigned direct-S3 URL. Used by the worker-spawn path.
func (a *apiSpawner) stageSelf(ctx context.Context) (string, error) {
	if a.cfg.Brain.UsesGateway() {
		return uploadViaConnector(ctx, a.cfg.Brain.GatewayURL)
	}
	return stageArtifact(ctx, a.cfg.Brain, artifactTTL)
}

// provisionAgent installs the sprite-agent service on a created sprite: its boot
// command fetches the binary from artifactURL and runs it with bootEnv. The caller
// stages the artifact first (workers stage self; init stages a cross-compiled binary).
func (a *apiSpawner) provisionAgent(ctx context.Context, name string, bootEnv map[string]string, artifactURL string) error {
	url := artifactURL
	env := map[string]string{"SPRITE_AGENT_ADDR": ":8080", "SPRITE_AGENT_WORKDIR": "/home/sprite"}

	// Anthropic auth via the API Gateway connector (DESIGN §3.2): point the
	// worker's Claude at the gateway base URL so it authenticates by the sprite's
	// own Fly identity — NO credential is copied onto the worker. The placeholder
	// key just satisfies Claude Code's "a key is set" check; the gateway injects
	// the real one. A fresh worker has no stored cred, so this is what lets it run
	// Claude on dispatched tasks.
	if base := gateway.AnthropicBaseURL(ctx); base != "" {
		env["ANTHROPIC_BASE_URL"] = base
		env["ANTHROPIC_API_KEY"] = "sprite-gateway"
	}

	// Optional fallback: copy the spawner's Claude credential to the worker. OFF
	// by default — the gateway above is preferred (no secret copied). Enable with
	// SPRITE_AGENT_PROPAGATE_CLAUDE_CREDS=1 only if no Anthropic connector exists.
	credURL := ""
	if os.Getenv("SPRITE_AGENT_PROPAGATE_CLAUDE_CREDS") == "1" {
		if u, err := stageClaudeCredential(ctx, a.cfg.Brain, artifactTTL); err == nil {
			credURL = u
		}
	}

	for k, v := range bootEnv {
		env[k] = v
	}
	boot := "set -e; "
	if credURL != "" {
		boot += "mkdir -p /home/sprite/.claude; " +
			"curl -fsSL '" + credURL + "' -o /home/sprite/.claude/.credentials.json; " +
			"chmod 600 /home/sprite/.claude/.credentials.json; "
	}
	boot += "curl -fsSL '" + url + "' -o /home/sprite/sprite-agent; " +
		"chmod +x /home/sprite/sprite-agent; cd /home/sprite; exec ./sprite-agent"
	body, err := json.Marshal(serviceSpec{
		Cmd: "/bin/sh", Args: []string{"-c", boot}, Dir: "/home/sprite", Env: env, HTTPPort: 8080,
	})
	if err != nil {
		return err
	}

	// Warm, install, confirm — retry once if the service didn't persist.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := a.warmSprite(ctx, name); err != nil {
			lastErr = err
			continue
		}
		if err := a.putService(ctx, name, body); err != nil {
			lastErr = err
			continue
		}
		if a.serviceExists(ctx, name) {
			return nil
		}
		lastErr = fmt.Errorf("service did not persist (sprite not ready)")
	}
	return lastErr
}

// warmSprite triggers warming (a freshly-created sprite is cold) and waits until
// its status is no longer cold, so subsequent service writes persist.
func (a *apiSpawner) warmSprite(ctx context.Context, name string) error {
	// A POST to exec warms the sprite (the body/output is irrelevant here).
	if resp, err := a.do(ctx, http.MethodPost, fmt.Sprintf("%s/v1/sprites/%s/exec", a.base, name), []byte(`{"command":"true"}`)); err == nil {
		resp.Body.Close()
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, err := a.spriteStatus(ctx, name)
		if err == nil && status != "" && status != "cold" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sprite %s did not warm (last status %q)", name, status)
		}
		interval := a.pollInterval
		if interval <= 0 {
			interval = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (a *apiSpawner) spriteStatus(ctx context.Context, name string) (string, error) {
	resp, err := a.do(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sprites/%s", a.base, name), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var s struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", err
	}
	return s.Status, nil
}

func (a *apiSpawner) putService(ctx context.Context, name string, body []byte) error {
	resp, err := a.do(ctx, http.MethodPut, fmt.Sprintf("%s/v1/sprites/%s/services/sprite-agent", a.base, name), body)
	if err != nil {
		return fmt.Errorf("install service: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("install service: %d: %s", resp.StatusCode, string(data))
	}
	return nil
}

func (a *apiSpawner) serviceExists(ctx context.Context, name string) bool {
	resp, err := a.do(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sprites/%s/services/sprite-agent", a.base, name), nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode/100 == 2
}

// Destroy deletes a sprite by name (used by the reaper).
func (a *apiSpawner) Destroy(ctx context.Context, name string) error {
	resp, err := a.do(ctx, http.MethodDelete, fmt.Sprintf("%s/v1/sprites/%s", a.base, name), nil)
	if err != nil {
		return fmt.Errorf("spawn: destroy %s: %w", name, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	// 404 = already gone; treat as success (idempotent reap).
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("spawn: destroy %s: %d: %s", name, resp.StatusCode, string(data))
	}
	return nil
}

// Exists reports whether a sprite still exists on the platform (a suspended sprite
// does; a destroyed one 404s). Used by the reaper to tell "suspended" from "gone".
func (a *apiSpawner) Exists(ctx context.Context, name string) (bool, error) {
	resp, err := a.do(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sprites/%s", a.base, name), nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("spawn: exists %s: %d", name, resp.StatusCode)
	}
	return true, nil
}

func (a *apiSpawner) do(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Token mode attaches the Bearer; connector mode sends none — the gateway
	// injects the credential by sprite identity.
	if a.cfg.SpriteAPIToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.SpriteAPIToken)
	}
	req.Header.Set("Content-Type", "application/json")
	return a.client.Do(req)
}
