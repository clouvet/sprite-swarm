package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
)

const updateCallTimeout = 20 * time.Second

// UpdateResult is the outcome of asking one peer to self-update.
type UpdateResult struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Status string `json:"status"`
}

// StageSelf publishes this agent's running binary to the brain (the same key the
// spawn path uses), so peers can fetch it. The propagate path stages before
// telling peers to update, so they pull the caller's current build.
func (s *Service) StageSelf(ctx context.Context) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	return s.brain.Put(ctx, config.ArtifactKey, data)
}

// PrepareSelfUpdate fetches the staged binary from the brain and, if it differs
// from the running build, verifies it and swaps it into place — but does NOT
// re-exec (so the HTTP handler can respond first, then call Reexec). Returns
// willUpdate=false when already current. The on-disk binary is replaced only
// after verification, and the old one is kept as <binary>.bak.
func (s *Service) PrepareSelfUpdate(ctx context.Context) (willUpdate bool, detail string, err error) {
	data, err := s.brain.Get(ctx, config.ArtifactKey)
	if err != nil {
		return false, "", fmt.Errorf("fetch staged binary: %w", err)
	}
	newBuild := hashBytes(data)
	if newBuild == s.build {
		return false, "already current (" + s.build + ")", nil
	}
	if err := verifyBinary(data); err != nil {
		return false, "", err
	}
	self, err := os.Executable()
	if err != nil {
		return false, "", err
	}
	if cur, err := os.ReadFile(self); err == nil {
		_ = os.WriteFile(self+".bak", cur, 0o755) // recovery copy if the new build is bad
	}
	tmp := self + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return false, "", err
	}
	if err := os.Rename(tmp, self); err != nil { // atomic swap on the same fs
		return false, "", err
	}
	return true, s.build + " -> " + newBuild, nil
}

// Reexec replaces the current process with the (already-swapped) on-disk binary,
// preserving args + env. Only returns on error (otherwise the image is replaced).
func (s *Service) Reexec() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(self, os.Args, os.Environ())
}

// UpdateFleet stages this agent's binary, then tells each target to self-update
// via a direct authenticated POST to its /api/fleet/update. target "" or "all"
// means every OTHER agent in the roster; otherwise the single matching id. The
// caller (e.g. home) never re-execs itself here.
func (s *Service) UpdateFleet(ctx context.Context, target string) (interface{}, error) {
	if err := s.StageSelf(ctx); err != nil {
		return nil, fmt.Errorf("stage binary: %w", err)
	}
	roster, err := s.roster(ctx)
	if err != nil {
		return nil, err
	}
	results := []UpdateResult{}
	for _, e := range roster {
		if e.ID == s.id {
			continue
		}
		if target != "" && target != "all" && e.ID != target {
			continue
		}
		r := UpdateResult{ID: e.ID}
		switch {
		case e.URL == "":
			r.Status = "no url"
		default:
			code, callErr := s.authedPost(ctx, strings.TrimRight(e.URL, "/")+"/api/fleet/update")
			switch {
			case callErr != nil:
				r.Status = callErr.Error()
			case code/100 == 2:
				r.OK, r.Status = true, "updating"
			default:
				r.Status = fmt.Sprintf("http %d", code)
			}
		}
		results = append(results, r)
	}
	return map[string]interface{}{"from_build": s.build, "targets": results}, nil
}

// ReloadFleet tells each target to re-read the brain and re-apply its env-based
// secrets (git/gh, flyctl) in place — no restart, no re-stage. Same fan-out as
// UpdateFleet; target "" or "all" means every OTHER agent, else the single match.
func (s *Service) ReloadFleet(ctx context.Context, target string) (interface{}, error) {
	roster, err := s.roster(ctx)
	if err != nil {
		return nil, err
	}
	results := []UpdateResult{}
	for _, e := range roster {
		if e.ID == s.id {
			continue
		}
		if target != "" && target != "all" && e.ID != target {
			continue
		}
		r := UpdateResult{ID: e.ID}
		switch {
		case e.URL == "":
			r.Status = "no url"
		default:
			code, callErr := s.authedPost(ctx, strings.TrimRight(e.URL, "/")+"/api/fleet/reload-secrets")
			switch {
			case callErr != nil:
				r.Status = callErr.Error()
			case code/100 == 2:
				r.OK, r.Status = true, "reloaded"
			default:
				r.Status = fmt.Sprintf("http %d", code)
			}
		}
		results = append(results, r)
	}
	return map[string]interface{}{"targets": results}, nil
}

// authedPost POSTs (empty body) to a peer URL with the sprites token as a Bearer.
func (s *Service) authedPost(ctx context.Context, url string) (int, error) {
	tok := s.GetSecret(ctx, SecretSpritesAPIToken)
	if tok == "" {
		return 0, fmt.Errorf("no sprites token to authenticate the call")
	}
	ctx, cancel := context.WithTimeout(ctx, updateCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// verifyBinary sanity-checks fetched bytes before we overwrite ourselves: a Linux
// ELF of plausible size. Cheap guard so a truncated/garbage download can't brick
// the agent (a bad swap would crash-loop the service).
func verifyBinary(data []byte) error {
	if len(data) < 1<<20 {
		return fmt.Errorf("staged binary too small (%d bytes)", len(data))
	}
	if data[0] != 0x7f || data[1] != 'E' || data[2] != 'L' || data[3] != 'F' {
		return fmt.Errorf("staged binary is not an ELF executable")
	}
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}
