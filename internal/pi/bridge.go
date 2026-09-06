package pi

import (
	"embed"
	"os"
	"path/filepath"
)

// bridgeFS holds the in-house MCP-bridge Pi extension (a TS module + its package.json).
// It's materialized to ~/.pi/agent/extensions/mcp-bridge at boot so pi auto-loads it and
// the Pi runtime gets the fleet's MCP servers as native tools. See bridge/index.ts.
//
//go:embed bridge/index.ts bridge/package.json
var bridgeFS embed.FS

// MaterializeBridge writes the bridge extension (index.ts + package.json) into destDir,
// creating it if needed. It only rewrites a file whose content differs, and reports
// whether package.json changed so the caller knows to (re)install the MCP client SDK.
func MaterializeBridge(destDir string) (pkgChanged bool, err error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return false, err
	}
	for _, name := range []string{"index.ts", "package.json"} {
		data, err := bridgeFS.ReadFile("bridge/" + name)
		if err != nil {
			return false, err
		}
		dst := filepath.Join(destDir, name)
		if old, _ := os.ReadFile(dst); string(old) == string(data) {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return false, err
		}
		if name == "package.json" {
			pkgChanged = true
		}
	}
	return pkgChanged, nil
}
