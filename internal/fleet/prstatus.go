package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// prCacheTTL is how long a branch's PR state is reused before a background refresh.
const prCacheTTL = 20 * time.Second

type prEntry struct {
	line    string
	fetched time.Time
}

var (
	prMu       sync.Mutex
	prCache    = map[string]prEntry{}
	prInflight = map[string]bool{}
)

// branchPRContext returns a one-line summary of the PR for the current branch of the
// git checkout at cwd, for the per-turn context — so a worker learns its branch's PR
// was merged/closed without being told. Empty when cwd is not a git checkout, is not
// on a branch, or has no PR.
//
// The git/gh lookup runs in the BACKGROUND and is cached: a cold or stale cache
// returns the last known value (possibly "") and kicks a refresh, so the per-turn
// hook never blocks on gh — it runs on every prompt, fleet-wide, and must stay fast.
// The state is therefore at most one turn / prCacheTTL stale, which is fine here.
func (s *Service) branchPRContext(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	prMu.Lock()
	entry, ok := prCache[cwd]
	if (!ok || time.Since(entry.fetched) > prCacheTTL) && !prInflight[cwd] {
		prInflight[cwd] = true
		go s.refreshPR(cwd)
	}
	line := entry.line
	prMu.Unlock()
	return line
}

func (s *Service) refreshPR(cwd string) {
	line := computeBranchPR(cwd)
	prMu.Lock()
	prCache[cwd] = prEntry{line: line, fetched: time.Now()}
	delete(prInflight, cwd)
	prMu.Unlock()
}

// computeBranchPR shells out to git + gh (both bounded) to render the PR line for the
// checkout's current branch. Any failure (not a repo, detached HEAD, no PR, gh not
// authed) yields "" — the caller treats "" as "nothing to say".
func computeBranchPR(cwd string) string {
	branch := strings.TrimSpace(runTool(cwd, 5*time.Second, "git", "rev-parse", "--abbrev-ref", "HEAD"))
	if branch == "" || branch == "HEAD" { // not a repo, or detached
		return ""
	}
	// `gh pr list --head <branch> --state all` finds the branch's PR by head-branch
	// name across ALL states — crucially, it still resolves a MERGED PR after its
	// branch was auto-deleted on merge (the exact case that matters here), which
	// `gh pr view` can't. Returns a JSON array (empty when the branch has no PR).
	out := strings.TrimSpace(runTool(cwd, 12*time.Second,
		"gh", "pr", "list", "--head", branch, "--state", "all",
		"--json", "number,title,state,mergedAt", "--limit", "1"))
	if out == "" {
		return ""
	}
	var prs []prInfo
	if json.Unmarshal([]byte(out), &prs) != nil || len(prs) == 0 {
		return ""
	}
	return formatPRLine(branch, prs[0])
}

// prInfo is the subset of `gh pr view --json` we render.
type prInfo struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	State    string `json:"state"` // OPEN | MERGED | CLOSED
	MergedAt string `json:"mergedAt"`
}

// formatPRLine renders the injected context line for a branch's PR (pure, tested).
func formatPRLine(branch string, pr prInfo) string {
	if pr.Number == 0 {
		return ""
	}
	title := pr.Title
	if len(title) > 80 {
		title = title[:80] + "…"
	}
	head := fmt.Sprintf("## Your branch %q → PR #%d %q is %s", branch, pr.Number, title, pr.State)
	switch pr.State {
	case "MERGED":
		when := ""
		if d, _, ok := strings.Cut(pr.MergedAt, "T"); ok && d != "" {
			when = " on " + d
		}
		return head + when + " — it is MERGED, not open. Don't act as if it's still under review; the work has landed.\n"
	case "CLOSED":
		return head + " — CLOSED without merging.\n"
	default: // OPEN
		return head + ".\n"
	}
}

// toolCandidates are the absolute paths we try first for each tool, because the
// service process PATH may omit the sprite tool dirs (gh lives in /.sprite/bin).
var toolCandidates = map[string][]string{
	"git": {"/usr/bin/git", "/bin/git"},
	"gh":  {"/.sprite/bin/gh", "/home/sprite/.local/bin/gh", "/usr/bin/gh"},
}

// runTool execs a tool in cwd with a bounded timeout, resolving it by absolute path
// (LookPath as a fallback). Returns stdout on success, "" on any error. gh auth comes
// from $HOME/.config/gh, inherited through the environment.
func runTool(cwd string, timeout time.Duration, name string, args ...string) string {
	bin := name
	for _, p := range toolCandidates[name] {
		if _, err := os.Stat(p); err == nil {
			bin = p
			break
		}
	}
	if bin == name {
		if p, err := exec.LookPath(name); err == nil {
			bin = p
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}
