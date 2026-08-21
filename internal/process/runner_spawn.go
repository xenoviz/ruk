package process

import (
	"context"
	"os"
	"os/exec"
)

// OSProcessSpawner is the default child spawner. Platform-specific command
// attributes are confined to configureCommand in runner_*.go.
type OSProcessSpawner struct{}

func (OSProcessSpawner) Spawn(ctx context.Context, request SpawnRequest) (Child, error) {
	return spawnOSProcess(ctx, request)
}

type osChild struct {
	command    *exec.Cmd
	foreground *foregroundTerminal
}

func (child *osChild) PID() int { return child.command.Process.Pid }

func (child *osChild) Wait() ExitStatus {
	err := child.command.Wait()
	status := processExitStatus(child.command.ProcessState, err)
	if child.foreground != nil {
		if restoreErr := child.foreground.restore(); restoreErr != nil {
			status.BoundaryError = restoreErr
		}
	}
	return status
}

func (child *osChild) Signal(signal os.Signal) error { return child.command.Process.Signal(signal) }
