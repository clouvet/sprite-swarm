package spawn

import (
	"strings"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
)

// A connector-mode fleet hands home a gateway URL, not a presigned one: the boot
// script re-fetches whenever the binary is missing, and a presigned URL is dead
// artifactTTL later.
func TestHomeArtifactURLPrefersGateway(t *testing.T) {
	u := connectorArtifactURL(config.BrainConfig{GatewayURL: "https://gw.example/s3/abc"}, artifactKey)
	if u != "https://gw.example/s3/abc/"+artifactKey {
		t.Fatalf("want gateway URL for the artifact key, got %q", u)
	}
	if strings.Contains(u, "X-Amz-") {
		t.Errorf("gateway URL must not be presigned: %q", u)
	}
}

// `init` runs off-account: it writes to the brain with raw keys but the fleet it
// ignites is connector-mode, so home is pointed at BootstrapGateway.
func TestHomeArtifactURLBootstrapGatewayOverrides(t *testing.T) {
	bc := config.BrainConfig{
		Bucket:           "brain",
		AccessKey:        "ak",
		SecretKey:        "sk",
		BootstrapGateway: "https://gw.example/s3/boot",
	}
	if u := connectorArtifactURL(bc, artifactKey); u != "https://gw.example/s3/boot/"+artifactKey {
		t.Fatalf("BootstrapGateway should win, got %q", u)
	}
}

// A trailing slash on the gateway base must not double up in the key path.
func TestHomeArtifactURLTrimsSlash(t *testing.T) {
	if u := connectorArtifactURL(config.BrainConfig{GatewayURL: "https://gw.example/s3/abc/"}, artifactKey); strings.Contains(u, "//fleet") {
		t.Fatalf("doubled slash in %q", u)
	}
}

// No connector: "" tells the caller to fall back to presigning, which is the only
// option on a keys-only fleet.
func TestHomeArtifactURLEmptyWithoutConnector(t *testing.T) {
	bc := config.BrainConfig{Bucket: "brain", AccessKey: "ak", SecretKey: "sk"}
	if u := connectorArtifactURL(bc, artifactKey); u != "" {
		t.Fatalf("keys-only fleet has no connector URL, got %q", u)
	}
}

// The URL home fetches through must be the gateway home is told to use —
// connectorArtifactURL and BootstrapEnv must not drift apart.
func TestHomeArtifactURLMatchesBootstrapEnvGateway(t *testing.T) {
	for _, bc := range []config.BrainConfig{
		{GatewayURL: "https://gw.example/s3/own"},
		{Bucket: "b", AccessKey: "ak", SecretKey: "sk", BootstrapGateway: "https://gw.example/s3/boot"},
		{GatewayURL: "https://gw.example/s3/own", BootstrapGateway: "https://gw.example/s3/boot"},
	} {
		env := BootstrapEnv(config.Config{Brain: bc}, "id")
		gw := env["SPRITE_AGENT_BRAIN_GATEWAY"]
		if gw == "" {
			t.Fatalf("BootstrapEnv gave no gateway for %+v", bc)
		}
		if u := connectorArtifactURL(bc, artifactKey); !strings.HasPrefix(u, gw+"/") {
			t.Errorf("artifact URL %q is not under the bootstrap gateway %q", u, gw)
		}
	}
}
