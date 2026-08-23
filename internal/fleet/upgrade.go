package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/clouvet/sprite-swarm/internal/buildinfo"
	"github.com/clouvet/sprite-swarm/internal/config"
)

// releaseRepo is the source of truth for released builds; releaseAssetName is the
// linux/amd64 binary the release workflow attaches to each release.
const (
	releaseRepo      = "clouvet/sprite-swarm"
	releaseAssetName = "sprite-agent"
	releaseCacheTTL  = 5 * time.Minute
)

// ReleaseInfo is the newest published release and (if attached) its binary asset.
type ReleaseInfo struct {
	Tag      string
	AssetURL string // download URL for the sprite-agent binary; "" if none attached
}

// UpgradeStatus is what the UI polls to decide whether to offer an upgrade.
type UpgradeStatus struct {
	Current    string `json:"current"`     // running version string (buildinfo.String)
	CurrentTag string `json:"current_tag"` // running release tag, or "dev"
	Latest     string `json:"latest"`      // latest release tag, "" if none/unreachable
	Available  bool   `json:"available"`   // a different release WITH a binary asset exists
}

var (
	relMu      sync.Mutex
	relCached  ReleaseInfo
	relFetched time.Time
)

// latestRelease fetches (cached) the newest GitHub release and its sprite-agent
// asset. sprite-swarm is public, so this is unauthenticated; releases/latest already
// excludes drafts + prereleases.
func latestRelease(ctx context.Context) (ReleaseInfo, error) {
	relMu.Lock()
	if relCached.Tag != "" && time.Since(relFetched) < releaseCacheTTL {
		info := relCached
		relMu.Unlock()
		return info, nil
	}
	relMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+releaseRepo+"/releases/latest", nil)
	if err != nil {
		return ReleaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return ReleaseInfo{}, fmt.Errorf("github releases/latest: status %d", resp.StatusCode)
	}
	var out struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ReleaseInfo{}, err
	}
	info := ReleaseInfo{Tag: out.TagName}
	for _, a := range out.Assets {
		if a.Name == releaseAssetName {
			info.AssetURL = a.URL
			break
		}
	}
	relMu.Lock()
	relCached, relFetched = info, time.Now()
	relMu.Unlock()
	return info, nil
}

// UpgradeStatus reports whether a newer release with a downloadable binary exists.
func (s *Service) UpgradeStatus(ctx context.Context) UpgradeStatus {
	st := UpgradeStatus{Current: buildinfo.String(), CurrentTag: buildinfo.Get().Tag}
	info, err := latestRelease(ctx)
	if err != nil {
		return st // latest unknown -> not offered
	}
	st.Latest = info.Tag
	st.Available = upgradeAvailable(info, st.CurrentTag)
	return st
}

// upgradeAvailable decides whether to offer an upgrade: the latest release must
// actually ship a binary AND carry a different tag than what we run (a "dev" build
// is always eligible to move onto a real release). Assetless releases (e.g. before
// the workflow attached a binary) are not upgradeable, so they aren't offered.
func upgradeAvailable(info ReleaseInfo, currentTag string) bool {
	return info.AssetURL != "" && info.Tag != "" && info.Tag != currentTag
}

// UpgradeStatusValue is the interface{}-returning wrapper (the server avoids
// importing the fleet package's types — mirrors EffectivePolicyValue etc.).
func (s *Service) UpgradeStatusValue(ctx context.Context) interface{} { return s.UpgradeStatus(ctx) }

// StageRelease downloads the latest release's sprite-agent binary, verifies it, and
// publishes it to the brain ArtifactKey — the slot self-update pulls from. Returns
// the staged release tag.
func (s *Service) StageRelease(ctx context.Context) (string, error) {
	info, err := latestRelease(ctx)
	if err != nil {
		return "", err
	}
	if info.AssetURL == "" {
		return "", fmt.Errorf("release %q has no %s binary attached", info.Tag, releaseAssetName)
	}
	data, err := downloadBinary(ctx, info.AssetURL)
	if err != nil {
		return "", fmt.Errorf("download release binary: %w", err)
	}
	if err := verifyBinary(data); err != nil {
		return "", err
	}
	if err := s.brain.Put(ctx, config.ArtifactKey, data); err != nil {
		return "", fmt.Errorf("stage release binary: %w", err)
	}
	return info.Tag, nil
}

// UpgradeFleet stages the latest release binary and tells every peer to self-update
// onto it — WITHOUT re-staging the caller's running binary (that's the difference
// from UpdateFleet). The caller self-updates separately, after its handler responds.
func (s *Service) UpgradeFleet(ctx context.Context) (interface{}, error) {
	tag, err := s.StageRelease(ctx)
	if err != nil {
		return nil, err
	}
	results, err := s.propagateUpdate(ctx, "all")
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"to": tag, "targets": results}, nil
}

func downloadBinary(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20)) // 200 MB cap
}
