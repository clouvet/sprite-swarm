package spawn

import "context"

// notConfigured is the Spawner used when no sprites API token is present. The
// capability exists and is addressable; the live call is stubbed so an
// unattended agent fails clearly rather than silently pretending to spawn.
type notConfigured struct{}

func (notConfigured) Available() bool { return false }

func (notConfigured) Spawn(_ context.Context, _ Request) (Result, error) {
	return Result{}, ErrNotConfigured
}

func (notConfigured) Destroy(_ context.Context, _ string) error {
	return ErrNotConfigured
}

func (notConfigured) Exists(_ context.Context, _ string) (bool, error) {
	return false, ErrNotConfigured
}
func (notConfigured) DeployApp(_ context.Context, _ DeployRequest) (Result, error) {
	return Result{}, ErrNotConfigured
}
