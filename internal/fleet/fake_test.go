package fleet

import (
	"context"
	"strings"
	"sync"
)

// fakeBrain is an in-memory Brain for tests — exercises the same
// register/roster logic that runs against S3/Tigris, without any network.
type fakeBrain struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeBrain() *fakeBrain { return &fakeBrain{objects: make(map[string][]byte)} }

func (f *fakeBrain) Put(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.objects[key] = cp
	return nil
}

func (f *fakeBrain) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return data, nil
}

func (f *fakeBrain) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

func (f *fakeBrain) List(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
