package spawn

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// spriteSwarmRepo is the public source the agent is built from for a ref spawn.
const spriteSwarmRepo = "https://github.com/clouvet/sprite-swarm.git"

var refSlug = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// refArtifactKey is the brain key a spawn-from-ref binary is staged under. It is
// DELIBERATELY distinct from config.ArtifactKey (the shared fleet build): a ref
// build must never clobber the fleet binary, so home and every normal worker stay
// on their release and ONLY a ref-spawned sprite ever runs the experimental build.
func refArtifactKey(ref string) string {
	return "fleet/artifacts/ref-" + strings.Trim(refSlug.ReplaceAllString(strings.TrimSpace(ref), "-"), "-") + "-sprite-agent"
}

// stageRef builds sprite-swarm at ref and stages the linux/amd64 binary under a
// ref-specific brain key, returning a URL the new sprite curls. Never touches the
// shared fleet artifact.
func (a *apiSpawner) stageRef(ctx context.Context, ref string) (string, error) {
	bin, cleanup, err := buildRefBinary(ctx, ref)
	if err != nil {
		return "", err
	}
	defer cleanup()
	key := refArtifactKey(ref)
	if a.cfg.Brain.UsesGateway() {
		return uploadFileViaConnector(ctx, a.cfg.Brain.GatewayURL, bin, key)
	}
	return stageFile(ctx, a.cfg.Brain, bin, key, artifactTTL)
}

// buildRefBinary clones sprite-swarm at ref (shallow) and cross-compiles the agent
// for linux/amd64. Returns the built binary path plus a cleanup func. The spawner
// host (home) has git + the Go toolchain. The ref is passed as a git argument (never
// a shell string), so it can't inject; the repo is fixed (the operator picks only
// the ref of our own repo).
func buildRefBinary(ctx context.Context, ref string) (string, func(), error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", func() {}, fmt.Errorf("spawn: empty ref")
	}
	dir, err := os.MkdirTemp("", "ss-ref-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	git := resolveTool("git", "/usr/bin/git", "/bin/git")
	if out, err := runCmd(ctx, 4*time.Minute, "", nil, git,
		"clone", "--depth", "1", "--branch", ref, spriteSwarmRepo, dir); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("spawn: clone %s@%s: %w: %s", spriteSwarmRepo, ref, err, out)
	}

	out := filepath.Join(dir, "sprite-agent")
	goBin := resolveTool("go", "/.sprite/languages/go/current/bin/go", "/.sprite/bin/go")
	env := append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if o, err := runCmd(ctx, 8*time.Minute, dir, env, goBin,
		"build", "-o", out, "./cmd/sprite-agent"); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("spawn: build %s: %w: %s", ref, err, o)
	}
	if _, err := os.Stat(out); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("spawn: built binary missing: %w", err)
	}
	return out, cleanup, nil
}

// resolveTool returns the first candidate absolute path that exists, else falls back
// to PATH lookup, else the bare name (the caller surfaces the exec error). The
// service PATH may omit /.sprite/bin, so absolute candidates come first.
func resolveTool(name string, candidates ...string) string {
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// runCmd runs bin+args (in dir, with env if non-nil) under a timeout, returning
// combined output. Used for the clone + build steps.
func runCmd(ctx context.Context, timeout time.Duration, dir string, env []string, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if len(out) > 2000 { // keep error logs bounded
		out = "…" + out[len(out)-2000:]
	}
	return out, err
}
