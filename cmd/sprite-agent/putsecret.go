package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
	"github.com/clouvet/sprite-agent/internal/fleet"
	"github.com/clouvet/sprite-agent/internal/gateway"
)

// runPutSecret writes one operational secret to the brain and exits — e.g. seeding
// or rotating the Claude subscription token (`claude-oauth-token`). On a sprite the
// brain is reached via the discovered s3 connector (token-free, by identity); off a
// sprite it falls back to S3 keys from the environment.
func runPutSecret(args []string) {
	fs := flag.NewFlagSet("put-secret", flag.ExitOnError)
	name := fs.String("name", "", "secret name, e.g. claude-oauth-token (required)")
	value := fs.String("value", "", "secret value (or use --file, or pipe via stdin)")
	file := fs.String("file", "", "read the value from this file")
	_ = fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "put-secret: --name is required")
		os.Exit(2)
	}
	val := *value
	switch {
	case *file != "":
		b, err := os.ReadFile(*file)
		if err != nil {
			log.Fatalf("put-secret: read %s: %v", *file, err)
		}
		val = string(b)
	case val == "":
		b, _ := io.ReadAll(os.Stdin)
		val = string(b)
	}
	val = strings.TrimSpace(val)
	if val == "" {
		log.Fatalf("put-secret: empty value (pass --value, --file, or pipe it in)")
	}

	cfg := config.FromEnv()
	// Discover the token-free brain connector by sprite identity, mirroring boot.
	if cfg.Brain.GatewayURL == "" && cfg.Brain.Bucket == "" {
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		if conns, err := gateway.Discover(dctx); err == nil {
			if c, ok := conns["s3_object_store"]; ok && c.GatewayBase != "" {
				cfg.Brain.GatewayURL = c.GatewayBase
			}
		}
		dcancel()
	}
	if !cfg.Brain.Enabled() {
		log.Fatalf("put-secret: no brain reachable (no s3 connector discovered and no S3 keys set)")
	}
	svc, err := fleet.New(cfg)
	if err != nil {
		log.Fatalf("put-secret: open brain: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.PutSecret(ctx, *name, val); err != nil {
		log.Fatalf("put-secret: write %q: %v", *name, err)
	}
	log.Printf("put-secret: wrote %q to the brain (%d chars)", *name, len(val))
}
