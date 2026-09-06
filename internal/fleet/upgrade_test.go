package fleet

import "testing"

func TestUpgradeAvailable(t *testing.T) {
	const asset = "https://example.com/sprite-agent"
	cases := []struct {
		name    string
		info    ReleaseInfo
		current string
		want    bool
	}{
		{"newer release from an older release", ReleaseInfo{Tag: "v0.2.0", AssetURL: asset}, "v0.1.1", true},
		{"newer patch", ReleaseInfo{Tag: "v0.1.6", AssetURL: asset}, "v0.1.5", true},
		// A dev build (home, or ANY branch-spawned/experimental sprite) is off-release
		// and must NOT be offered a release — that's the reported bug.
		{"dev build is NOT offered a release", ReleaseInfo{Tag: "v0.1.1", AssetURL: asset}, "dev", false},
		{"same tag", ReleaseInfo{Tag: "v0.1.1", AssetURL: asset}, "v0.1.1", false},
		{"older release is not offered (no downgrade)", ReleaseInfo{Tag: "v0.1.1", AssetURL: asset}, "v0.2.0", false},
		{"newer release but no binary", ReleaseInfo{Tag: "v0.2.0", AssetURL: ""}, "v0.1.1", false},
		{"no release at all", ReleaseInfo{Tag: "", AssetURL: ""}, "dev", false},
		{"malformed current tag", ReleaseInfo{Tag: "v0.2.0", AssetURL: asset}, "v0.2", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := upgradeAvailable(c.info, c.current); got != c.want {
				t.Errorf("upgradeAvailable(%+v, %q) = %v, want %v", c.info, c.current, got, c.want)
			}
		})
	}
}
