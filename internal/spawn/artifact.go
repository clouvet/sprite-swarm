package spawn

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/clouvet/sprite-agent/internal/config"
)

// artifactKey is where a spawner stages its own binary in the brain bucket so a
// freshly-created sprite can fetch it. Arch-tagged since the staged binary must
// match the target (same platform as the spawner in practice).
const artifactKey = "fleet/artifacts/sprite-agent-linux-amd64"

// credentialKey stages the Claude OAuth credential so a worker's `claude` can
// authenticate (a fresh sprite has none). See provisionAgent for the tradeoff.
const credentialKey = "fleet/artifacts/claude-credentials.json"

// stageArtifact uploads the spawner's own binary to the brain bucket and returns
// a presigned GET URL the new sprite can curl without needing S3 credentials to
// download (it still gets S3 creds via the bootstrap env, for the brain).
//
// This is the piece that makes a spawned sprite run *this same artifact*
// (DESIGN §4.2): exec/fs are control-ws (SDK-only), but the declarative services
// API + a presigned URL provision over plain REST.
func stageArtifact(ctx context.Context, bc config.BrainConfig, expires time.Duration) (string, error) {
	if !bc.Enabled() {
		return "", fmt.Errorf("spawn: cannot provision without a brain (S3) to stage the binary")
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("spawn: locate own binary: %w", err)
	}
	return stageFile(ctx, bc, self, artifactKey, expires)
}

// stageClaudeCredential stages the local Claude credential (if present) and
// returns a presigned URL, or "" if there's no credential to propagate.
func stageClaudeCredential(ctx context.Context, bc config.BrainConfig, expires time.Duration) (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/sprite"
	}
	path := home + "/.claude/.credentials.json"
	if _, err := os.Stat(path); err != nil {
		return "", nil // nothing to propagate
	}
	return stageFile(ctx, bc, path, credentialKey, expires)
}

// stageFile uploads localPath to key in the brain bucket and presigns a GET URL.
func stageFile(ctx context.Context, bc config.BrainConfig, localPath, key string, expires time.Duration) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("spawn: open %s: %w", localPath, err)
	}
	defer f.Close()

	client, err := s3ClientFor(ctx, bc)
	if err != nil {
		return "", err
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bc.Bucket),
		Key:    aws.String(key),
		Body:   f,
	}); err != nil {
		return "", fmt.Errorf("spawn: stage %s: %w", key, err)
	}

	ps := s3.NewPresignClient(client)
	req, err := ps.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bc.Bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("spawn: presign %s: %w", key, err)
	}
	return req.URL, nil
}

func s3ClientFor(ctx context.Context, bc config.BrainConfig) (*s3.Client, error) {
	region := bc.Region
	if region == "" {
		region = "auto"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(bc.AccessKey, bc.SecretKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("spawn: load aws config: %w", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if bc.Endpoint != "" {
			o.BaseEndpoint = aws.String(bc.Endpoint)
		}
		o.UsePathStyle = true
	}), nil
}
