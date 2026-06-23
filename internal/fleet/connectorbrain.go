package fleet

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// connectorBrain reaches the brain bucket through the Sprite API Gateway
// s3_object_store connector — authenticated by the sprite's own Fly identity, so
// NO S3 credentials live on the agent. This is what makes brain access token-free
// and keeps every sprite symmetric: any org sprite can reach the brain with
// nothing but its identity + the (non-secret) connector base URL.
//
// Verified the connector proxies GET/PUT/DELETE and ListObjectsV2 against the
// brain bucket. (Presign isn't available — and isn't needed: the spawner uploads
// the worker binary via PUT and the worker downloads it via GET, both here.)
type connectorBrain struct {
	base   string // e.g. https://api.sprites.dev/v1/gateway/s3_object_store/<id>
	client *http.Client
}

func newConnectorBrain(base string) *connectorBrain {
	return &connectorBrain{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (b *connectorBrain) objURL(key string) string {
	return b.base + "/" + strings.TrimPrefix(key, "/")
}

func (b *connectorBrain) Put(ctx context.Context, key string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, b.objURL(key), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("brain put %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("brain put %s: %d: %s", key, resp.StatusCode, string(body))
	}
	return nil
}

func (b *connectorBrain) Get(ctx context.Context, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.objURL(key), nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brain get %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("brain get %s: %d: %s", key, resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

func (b *connectorBrain) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.objURL(key), nil)
	if err != nil {
		return err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("brain delete %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("brain delete %s: %d: %s", key, resp.StatusCode, string(body))
	}
	return nil
}

// listBucketResult is the S3 ListObjectsV2 XML response shape.
type listBucketResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

func (b *connectorBrain) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("prefix", prefix)
		if token != "" {
			q.Set("continuation-token", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("brain list %s: %w", prefix, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("brain list %s: %d: %s", prefix, resp.StatusCode, string(body))
		}
		var lr listBucketResult
		if err := xml.Unmarshal(body, &lr); err != nil {
			return nil, fmt.Errorf("brain list %s: parse: %w", prefix, err)
		}
		for _, c := range lr.Contents {
			keys = append(keys, c.Key)
		}
		if !lr.IsTruncated || lr.NextContinuationToken == "" {
			break
		}
		token = lr.NextContinuationToken
	}
	return keys, nil
}
