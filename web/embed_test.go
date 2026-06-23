package web

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedJSParses guards against shipping a JavaScript syntax error in the
// embedded UI — a parse error aborts the whole script and silently breaks the
// chat, which neither `go build` nor the WS/REST tests can catch (they never load
// the page). Runs `node --check` on each embedded .js. Skips if node is absent.
func TestEmbeddedJSParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found; skipping JS syntax guard")
	}
	root := FS()
	err = fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		data, rerr := fs.ReadFile(root, path)
		if rerr != nil {
			return rerr
		}
		tmp, werr := os.CreateTemp(t.TempDir(), "*.js")
		if werr != nil {
			return werr
		}
		if _, werr := tmp.Write(data); werr != nil {
			return werr
		}
		tmp.Close()
		out, cerr := exec.Command(node, "--check", tmp.Name()).CombinedOutput()
		if cerr != nil {
			t.Errorf("embedded %s has a JS syntax error:\n%s", filepath.Base(path), string(out))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded assets: %v", err)
	}
}
