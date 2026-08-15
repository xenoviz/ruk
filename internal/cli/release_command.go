package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

// RepositoryReleaseResult is the lifecycle result needed by the public
// release command. Repository discovery and release orchestration stay outside
// this rendering/validation layer.
type RepositoryReleaseResult struct {
	Workspace        state.WorkspaceRecord
	CleanedProcesses int
}

// RepositoryReleaseOperation is the repository-aware release seam. The force
// policy is passed explicitly so command tests never need Git or process
// subprocesses.
type RepositoryReleaseOperation func(context.Context, git.Repository, string, bool) (RepositoryReleaseResult, error)

// ReleaseOperation is the concise compatibility name for the release seam.
type ReleaseOperation = RepositoryReleaseOperation

// ReleaseInput contains the validated public release options.
type ReleaseInput struct {
	Repository   git.Repository
	AssignmentID string
	Force        bool
	JSON         bool
}

// ReleaseRecord is the stable machine-readable release result.
type ReleaseRecord struct {
	Status           string `json:"status"`
	AssignmentID     string `json:"assignmentId"`
	Path             string `json:"path"`
	CleanedProcesses int    `json:"cleanedProcesses"`
}

// ReleaseResult includes the validated record and its selected rendering.
type ReleaseResult struct {
	ReleaseRecord
	Output string
}

// Release validates the lifecycle result and formats the TypeScript-compatible
// human or JSON success output. Failed operations never produce success text.
func Release(ctx context.Context, input ReleaseInput, operation RepositoryReleaseOperation) (ReleaseResult, error) {
	if input.AssignmentID == "" {
		return ReleaseResult{}, errors.New("assignment ID must not be empty")
	}
	if operation == nil {
		return ReleaseResult{}, errors.New("release operation is not configured")
	}
	result, err := operation(ctx, input.Repository, input.AssignmentID, input.Force)
	if err != nil {
		return ReleaseResult{}, err
	}
	if result.Workspace.Path == "" {
		return ReleaseResult{}, errors.New("release operation returned a workspace without a path")
	}
	if result.Workspace.Lifecycle != state.LifecycleAvailable {
		return ReleaseResult{}, fmt.Errorf("release operation returned workspace in %s state, expected available", result.Workspace.Lifecycle)
	}
	if result.Workspace.Assignment != nil {
		return ReleaseResult{}, errors.New("release operation returned an available workspace with an assignment")
	}
	if result.CleanedProcesses < 0 {
		return ReleaseResult{}, errors.New("release operation returned a negative cleaned process count")
	}

	record := ReleaseRecord{
		Status:           "available",
		AssignmentID:     input.AssignmentID,
		Path:             result.Workspace.Path,
		CleanedProcesses: result.CleanedProcesses,
	}
	var output string
	if input.JSON {
		encoded, err := json.Marshal(record)
		if err != nil {
			return ReleaseResult{}, fmt.Errorf("encode release result: %w", err)
		}
		output = string(encoded) + "\n"
	} else {
		output = fmt.Sprintf("Released %s\n", record.Path)
	}
	return ReleaseResult{ReleaseRecord: record, Output: output}, nil
}
