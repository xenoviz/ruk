package process

import "context"

// OSProcessSpawner is the default child spawner. Platform-specific command
// attributes are confined to configureCommand in runner_*.go.
type OSProcessSpawner struct{}

func (OSProcessSpawner) Spawn(ctx context.Context, request SpawnRequest) (Child, error) {
	return spawnOSProcess(ctx, request)
}
