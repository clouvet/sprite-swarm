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
	f, err := os.Open(self)
	if err != nil {
		return "", fmt.Errorf("spawn: open own binary: %w", err)
	}
	defer f.Close()

	client, err := s3ClientFor(ctx, bc)
	if err != nil {
		return "", err
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bc.Bucket),
		Key:    aws.String(artifactKey),
		Body:   f,
	}); err != nil {
		return "", fmt.Errorf("spawn: stage artifact: %w", err)
	}

	ps := s3.NewPresignClient(client)
	req, err := ps.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bc.Bucket),
		Key:    aws.String(artifactKey),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("spawn: presign artifact: %w", err)
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
