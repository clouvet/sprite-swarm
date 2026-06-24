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
#     --sprites-token <token> [--github-token <token>] [--fly-token <token>]
#
# Note: the brain bucket will STORE your Sprites/GitHub/Fly tokens so every worker
# reconstitutes from it — guard the bucket's keys + connector.

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
