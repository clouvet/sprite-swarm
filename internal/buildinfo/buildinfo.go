// Package buildinfo exposes a human-meaningful version for the running binary.
//
// The roster's fleet "build" is a content hash of the binary (good for staleness
// and self-update dedup, but opaque — it can't tell you which commit or release a
// sprite runs). buildinfo fills that gap: a release Tag stamped at build time, plus
// the commit SHA/time that the Go toolchain embeds automatically.
//
//   - Tag comes from -ldflags "-X github.com/clouvet/sprite-swarm/internal/buildinfo.Tag=v0.1.0".
//     Releases (built by the installer from a git tag) set it; ad-hoc `go build`
//     leaves it "dev". That is the intended split: main HEAD builds are "dev", only
//     tagged releases carry a version.
//   - Commit/CommitTime/Dirty come from debug.ReadBuildInfo() (vcs.* settings), which
//     `go build` stamps from the .git checkout with no ldflags needed.
package buildinfo

import (
	"runtime/debug"
	"sync"
)

// Tag is the release tag, set via -ldflags at build time. "dev" for un-tagged
// (ad-hoc / main HEAD) builds.
var Tag = "dev"

// Info is the resolved version of the running binary.
type Info struct {
	Tag        string `json:"tag"`         // release tag, or "dev"
	Commit     string `json:"commit"`      // short git SHA, or "" if unknown
	CommitTime string `json:"commit_time"` // RFC3339 commit time, or ""
	Dirty      bool   `json:"dirty"`       // built from a modified working tree
}

var (
	once   sync.Once
	cached Info
)

// Get returns the running binary's version, reading VCS build settings once.
func Get() Info {
	once.Do(func() {
		cached = Info{Tag: Tag}
		bi, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) >= 12 {
					cached.Commit = s.Value[:12]
				} else {
					cached.Commit = s.Value
				}
			case "vcs.time":
				cached.CommitTime = s.Value
			case "vcs.modified":
				cached.Dirty = s.Value == "true"
			}
		}
	})
	return cached
}

// String renders a compact version for logs and the roster, e.g.
// "v0.1.0 (a1b2c3d4e5f6)", "dev (a1b2c3d4e5f6)", or "dev (a1b2c3d4e5f6, dirty)".
func String() string {
	i := Get()
	s := i.Tag
	if i.Commit != "" {
		s += " (" + i.Commit
		if i.Dirty {
			s += ", dirty"
		}
		s += ")"
	} else if i.Dirty {
		s += " (dirty)"
	}
	return s
}
