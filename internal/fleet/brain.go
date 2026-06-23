package fleet

import "context"

// Object is a key/value pair in the brain.
type Object struct {
	Key  string
	Data []byte
}

// Brain is the storage capability the fleet needs: write your own keys, read
// any key, list a prefix. Kept as an interface so the roster/registration logic
// is testable against an in-memory fake and the live S3/Tigris client is one
// swappable implementation (DESIGN §4: derive-the-index over per-writer keys).
type Brain interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
}
