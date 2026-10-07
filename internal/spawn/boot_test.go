package spawn

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runBoot runs script under sh with a stub curl on PATH. curlScript is the stub's
// body; it receives curl's args.
func runBoot(t *testing.T, script, curlScript string) (string, error) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\n"+curlScript), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAgentBootRunsExistingBinaryWhenURLExpired(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sprite-agent"), []byte("#!/bin/sh\necho agent-ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runBoot(t, agentBootScript(dir, "https://expired", "https://expired-cred"), "exit 22\n")
	if err != nil || !strings.Contains(out, "agent-ran") {
		t.Fatalf("want existing binary to run despite 403; err=%v out=%q", err, out)
	}
}

func TestAgentBootFetchesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	// Stub curl writes a runnable agent to the -o path (last arg).
	curl := `for a; do o=$a; done; printf '#!/bin/sh\necho fetched\n' > "$o"` + "\n"
	out, err := runBoot(t, agentBootScript(dir, "https://ok", ""), curl)
	if err != nil || !strings.Contains(out, "fetched") {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

func TestAgentBootFailsWhenMissingAndFetchFails(t *testing.T) {
	dir := t.TempDir()
	if _, err := runBoot(t, agentBootScript(dir, "https://expired", ""), "exit 22\n"); err == nil {
		t.Fatal("want failure with no binary and a failed fetch")
	}
	if _, err := os.Stat(filepath.Join(dir, "sprite-agent")); err == nil {
		t.Fatal("a failed fetch must not leave a sprite-agent behind")
	}
}

// appBoot returns the app service's boot script with its app dir moved to dir.
func appBoot(t *testing.T, dir string) string {
	t.Helper()
	body, err := appServiceSpec(DeployRequest{ArtifactURL: "https://expired", Run: "cat index.html"})
	if err != nil {
		t.Fatal(err)
	}
	var spec serviceSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(spec.Args[1], "/home/sprite/app", dir)
}

func TestAppBootKeepsExistingFilesWhenURLExpired(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("old-app"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runBoot(t, appBoot(t, dir), "exit 22\n")
	if err != nil || !strings.Contains(out, "old-app") {
		t.Fatalf("want existing app to run despite 403; err=%v out=%q", err, out)
	}
}

func TestAppBootFailsWhenEmptyAndFetchFails(t *testing.T) {
	if _, err := runBoot(t, appBoot(t, t.TempDir()), "exit 22\n"); err == nil {
		t.Fatal("want failure with no app files and a failed fetch")
	}
}
