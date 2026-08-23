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
		{"newer release with binary", ReleaseInfo{Tag: "v0.2.0", AssetURL: asset}, "v0.1.1", true},
		{"dev build, release has binary", ReleaseInfo{Tag: "v0.1.1", AssetURL: asset}, "dev", true},
		{"same tag", ReleaseInfo{Tag: "v0.1.1", AssetURL: asset}, "v0.1.1", false},
		{"newer release but no binary", ReleaseInfo{Tag: "v0.2.0", AssetURL: ""}, "v0.1.1", false},
		{"no release at all", ReleaseInfo{Tag: "", AssetURL: ""}, "dev", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := upgradeAvailable(c.info, c.current); got != c.want {
				t.Errorf("upgradeAvailable(%+v, %q) = %v, want %v", c.info, c.current, got, c.want)
			}
		})
	}
}
