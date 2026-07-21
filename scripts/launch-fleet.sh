#!/usr/bin/env bash
#
# launch-fleet.sh — stand up a brand-new sprite-agent fleet.
#
# Standing up a fleet = prime the brain (stage the binary + write the operational
# secrets) and ignite one home sprite; everything else reconstitutes from the brain.
#
# Pre-reqs (one-time, in the Sprites dashboard):
#   - a Tigris bucket + an `s3_object_store` connector pointing at it (the brain)
#   - an `anthropic` connector (so sprites get Claude without a copied key)
# You provide the bucket's direct S3 keys here so this can run from anywhere
# (laptop). The sprites read the same bucket via the connector at runtime.
#
# Requires: Go (to cross-compile the linux/amd64 binary) and this repo.
#
# Usage:
#   scripts/launch-fleet.sh \
#     --name my-fleet \
#     --bucket <tigris-bucket> --s3-access-key <key> --s3-secret-key <secret> \
#     [--s3-endpoint https://fly.storage.tigris.dev] [--s3-region auto] \
#     --sprites-token <token> [--github-token <token>] [--fly-token <token>] \
#     [--claude-oauth-token <token>] [--discourse-profile <file.json>] \
#     [--brain-gateway <s3_object_store connector URL>]
#
# --brain-gateway: run the fleet TOKEN-FREE. Pass your `s3_object_store` connector's
# gateway URL (https://api.sprites.dev/v1/gateway/s3_object_store/<id>) and the
# sprites will reach the brain by their own identity — no S3 keys copied onto them.
# You still pass --s3-access-key/--s3-secret-key here (this launch host isn't a
# sprite, so it primes the brain with the keys); the running fleet just doesn't
# carry them. Omit the flag for the simpler key-based fleet.
#
# --claude-oauth-token: a Claude subscription token from `claude setup-token` (run
# once on a machine with a browser). When set, the fleet drives Claude through your
# subscription instead of the metered API connector. Override per sprite with the
# env var SPRITE_AGENT_CLAUDE_AUTH=connector to fall back to the API.
#
# --discourse-profile: a @discourse/mcp profile JSON — {"auth_pairs":[{"site":..,
# "api_key":..,"api_username":..}]}. When set, the fleet gains read-only access to
# those Discourse forums (paste a topic link and Claude pulls the thread in). One
# profile can list several sites. Seed/rotate later with `sprite-agent put-secret
# --name discourse --file <file>`.
#
# Note: the brain bucket will STORE your Sprites/GitHub/Fly/Claude/Discourse tokens
# so every worker reconstitutes from it — guard the bucket's keys + connector.

set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> Cross-compiling the linux/amd64 artifact..."
artifact="$(mktemp -t sprite-agent.XXXXXX)"
GOOS=linux GOARCH=amd64 go build -o "$artifact" ./cmd/sprite-agent
trap 'rm -f "$artifact" "$initbin"' EXIT

echo "==> Building the init helper for this machine..."
initbin="$(mktemp -t sprite-agent-init.XXXXXX)"
go build -o "$initbin" ./cmd/sprite-agent

echo "==> Priming the brain + igniting home..."
exec "$initbin" init --artifact "$artifact" "$@"
