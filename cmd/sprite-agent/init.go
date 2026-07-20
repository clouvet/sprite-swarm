package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
	"github.com/clouvet/sprite-swarm/internal/fleet"
	"github.com/clouvet/sprite-swarm/internal/spawn"
)

// runInit stands up a brand-new fleet: prime the brain (stage the artifact + write
// the operational secrets, via direct Tigris S3 keys so it works off-account), then
// ignite the home sprite. Driven by launch-fleet.sh. Everything else reconstitutes
// from the brain — workers rehydrate the secrets and self-discover the connectors.
func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	bucket := fs.String("bucket", "", "Tigris brain bucket (required)")
	endpoint := fs.String("s3-endpoint", "https://fly.storage.tigris.dev", "S3 endpoint")
	region := fs.String("s3-region", "auto", "S3 region")
	accessKey := fs.String("s3-access-key", "", "Tigris access key (required)")
	secretKey := fs.String("s3-secret-key", "", "Tigris secret key (required)")
	spritesToken := fs.String("sprites-token", "", "Sprites API token (optional; worker spawn/reap is disabled until one is set)")
	githubToken := fs.String("github-token", "", "GitHub token (optional)")
	flyToken := fs.String("fly-token", "", "Fly token (optional)")
	claudeToken := fs.String("claude-oauth-token", "", "Claude subscription OAuth token from `claude setup-token` (optional; the fleet defaults to it over the API connector)")
	discourseProfile := fs.String("discourse-profile", "", "path to a @discourse/mcp profile JSON (optional; enables read-only Discourse forum access fleet-wide)")
	name := fs.String("name", "", "home sprite name (required)")
	artifact := fs.String("artifact", "", "path to a linux/amd64 sprite-agent binary to stage (required)")
	_ = fs.Parse(args)

	required := map[string]string{
		"bucket": *bucket, "s3-access-key": *accessKey, "s3-secret-key": *secretKey,
		"name": *name, "artifact": *artifact,
	}
	var missing []string
	for k, v := range required {
		if v == "" {
			missing = append(missing, "--"+k)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "init: missing required flags: %s\n", strings.Join(missing, ", "))
		os.Exit(2)
	}
	if _, err := os.Stat(*artifact); err != nil {
		fmt.Fprintf(os.Stderr, "init: artifact not found: %s\n", *artifact)
		os.Exit(2)
	}

	cfg := config.Config{
		SpriteAPIToken: *spritesToken,
		Brain: config.BrainConfig{
			Bucket: *bucket, Endpoint: *endpoint, Region: *region,
			AccessKey: *accessKey, SecretKey: *secretKey,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// 1. Prime the brain: write the operational secrets (direct S3 keys).
	svc, err := fleet.New(cfg)
	if err != nil {
		log.Fatalf("init: open brain: %v", err)
	}
	if *spritesToken != "" {
		if err := svc.PutSecret(ctx, fleet.SecretSpritesAPIToken, *spritesToken); err != nil {
			log.Fatalf("init: write sprites token (check bucket/keys/endpoint): %v", err)
		}
	} else {
		log.Printf("init: no --sprites-token — the fleet will chat, but worker spawn/reap is disabled " +
			"until one is added (sprite-agent put-secret --name sprites-api-token)")
	}
	if *githubToken != "" {
		_ = svc.PutSecret(ctx, fleet.SecretGitHubToken, *githubToken)
	}
	if *flyToken != "" {
		_ = svc.PutSecret(ctx, fleet.SecretFlyToken, *flyToken)
	}
	if *claudeToken != "" {
		_ = svc.PutSecret(ctx, fleet.SecretClaudeOAuthToken, *claudeToken)
	}
	if *discourseProfile != "" {
		prof, err := os.ReadFile(*discourseProfile)
		if err != nil {
			log.Fatalf("init: read discourse profile %s: %v", *discourseProfile, err)
		}
		if err := svc.PutSecret(ctx, fleet.SecretDiscourse, string(prof)); err != nil {
			log.Fatalf("init: write discourse profile: %v", err)
		}
	}
	log.Printf("init: secrets written to brain (s3://%s)", *bucket)

	// 2. Ignite home: stage the artifact + create + provision the sprite as home.
	res, err := spawn.LaunchHome(ctx, cfg, *artifact, *name)
	if err != nil {
		log.Fatalf("init: launch home: %v", err)
	}

	fmt.Println()
	fmt.Printf("✅ Fleet home launched: %s\n", res.URL)
	fmt.Printf("   sprite: %s\n\n", res.Name)
	fmt.Println("   The brain bucket now stores your Sprites/GitHub/Fly tokens so every worker")
	fmt.Println("   reconstitutes from it. Guard the bucket's S3 keys + its s3 connector — that")
	fmt.Println("   is the trust boundary for the whole fleet.")
}
