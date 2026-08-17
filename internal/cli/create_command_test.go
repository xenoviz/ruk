package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/git"
)

type createWorkspaceStub struct {
	created       bool
	removed       bool
	path          string
	branch        string
	start         string
	detach        bool
	remove        error
	removeContext context.Context
}

type failingCreateWriter struct{ err error }

func (writer failingCreateWriter) Write([]byte) (int, error) { return 0, writer.err }

func (stub *createWorkspaceStub) Create(_ context.Context, path, branch, start string, detach bool) error {
	stub.created = true
	stub.path, stub.branch, stub.start, stub.detach = path, branch, start, detach
	return nil
}

func (stub *createWorkspaceStub) Remove(ctx context.Context, path string, force bool) error {
	stub.removed = true
	stub.removeContext = ctx
	if !force {
		return errors.New("cleanup was not forced")
	}
	if path != stub.path {
		return errors.New("cleanup path changed")
	}
	return stub.remove
}

func TestCreateCommandRollbackUsesBoundedRecoveryContextAfterCancellation(t *testing.T) {
	workspace := &createWorkspaceStub{}
	ctx, cancel := context.WithCancel(context.Background())
	command := createTestCommand(workspace, func(syncCtx context.Context, _ CreateSyncRequest) (SyncCommandResult, error) {
		cancel()
		return SyncCommandResult{}, syncCtx.Err()
	})

	_, err := command.Run(ctx, CreateCommandInput{Repository: createTestRepository(t), Branch: "agent/canceled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want original cancellation", err)
	}
	if !workspace.removed || workspace.removeContext == nil {
		t.Fatalf("workspace=%#v, want recovery cleanup", workspace)
	}
	if err := workspace.removeContext.Err(); err != nil {
		t.Fatalf("recovery context error=%v, want active context", err)
	}
	if _, ok := workspace.removeContext.Deadline(); !ok {
		t.Fatal("recovery cleanup context has no bounded deadline")
	}
}

func createTestRepository(t *testing.T) git.Repository {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	return git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
}

func createTestCommand(workspace CreateWorkspace, sync CreateSyncOperation) *CreateCommand {
	return &CreateCommand{
		Workspace: workspace,
		StartPoint: func(_ context.Context, _ git.Repository, requested string, fetch bool) (string, error) {
			if requested == "" && !fetch {
				return "HEAD", nil
			}
			return requested, nil
		},
		Sync: sync,
	}
}

func TestCreateCommandPreservesOptionsDefaultPathAndHumanOutput(t *testing.T) {
	workspace := &createWorkspaceStub{}
	var syncRequest CreateSyncRequest
	command := createTestCommand(workspace, func(_ context.Context, request CreateSyncRequest) (SyncCommandResult, error) {
		syncRequest = request
		return SyncCommandResult{Status: "prepared", Fingerprint: "0123456789abcdef", Mode: "managed-install"}, nil
	})
	var output bytes.Buffer
	repository := createTestRepository(t)
	result, err := command.Run(context.Background(), CreateCommandInput{
		Repository: repository, CWD: filepath.Join(filepath.Dir(repository.Root), "caller"), Branch: "agent/ui/header", Output: &output,
	})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	wantPath := filepath.Join(filepath.Dir(repository.Root), filepath.Base(repository.Root)+"-agent-ui-header")
	if result.Path != wantPath || workspace.path != wantPath || workspace.branch != "agent/ui/header" || workspace.start != "HEAD" || workspace.detach {
		t.Fatalf("result=%#v workspace=%#v, want default create contract", result, workspace)
	}
	if syncRequest.Repository.Root != wantPath || syncRequest.JSON {
		t.Fatalf("sync request = %#v", syncRequest)
	}
	wantOutput := "Dependencies prepared for 0123456789ab (managed-install).\n" + wantPath + "\n"
	if output.String() != wantOutput || result.Output != wantOutput {
		t.Fatalf("output=%q result.Output=%q, want %q", output.String(), result.Output, wantOutput)
	}
}

func TestCreateCommandUsesRequestedPathStartPointFetchAndDetach(t *testing.T) {
	workspace := &createWorkspaceStub{}
	startCalled := false
	command := &CreateCommand{
		Workspace: workspace,
		StartPoint: func(_ context.Context, repository git.Repository, requested string, fetch bool) (string, error) {
			startCalled = repository.Root != "" && requested == "origin/main" && fetch
			return "refs/remotes/origin/main", nil
		},
		Sync: func(_ context.Context, request CreateSyncRequest) (SyncCommandResult, error) {
			return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
		},
	}
	repository := createTestRepository(t)
	requested := filepath.Join(t.TempDir(), "explicit-create")
	result, err := command.Run(context.Background(), CreateCommandInput{
		Repository: repository, CWD: filepath.Join(t.TempDir(), "caller"), Branch: "release/v1",
		Path: requested, From: "origin/main", Fetch: true, Detach: true,
	})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if !startCalled || workspace.path != requested || workspace.start != "refs/remotes/origin/main" || !workspace.detach || result.Path != requested {
		t.Fatalf("startCalled=%v workspace=%#v result=%#v", startCalled, workspace, result)
	}
}

func TestCreateCommandJSONEmitsOneSyncRecordWithoutHumanDestination(t *testing.T) {
	workspace := &createWorkspaceStub{}
	command := createTestCommand(workspace, func(_ context.Context, request CreateSyncRequest) (SyncCommandResult, error) {
		if !request.JSON {
			return SyncCommandResult{}, errors.New("sync was not JSON")
		}
		return SyncCommandResult{Status: "ready", Fingerprint: "fingerprint", Mode: "bun-global-store", Reused: true, AlreadyAttached: true}, nil
	})
	var output bytes.Buffer
	repository := createTestRepository(t)
	result, err := command.Run(context.Background(), CreateCommandInput{
		Repository: repository, Branch: "agent/json", JSON: true, Output: &output,
	})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	var decoded CreateCommandResult
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON output=%q: %v", output.String(), err)
	}
	if decoded.Path != result.Path || decoded.Status != "ready" || decoded.Fingerprint != "fingerprint" || decoded.Mode != "bun-global-store" {
		t.Fatalf("decoded=%#v result=%#v", decoded, result)
	}
	if !result.Reused || !result.AlreadyAttached {
		t.Fatalf("result=%#v, want dependency reuse metadata", result)
	}
	if strings.Contains(output.String(), "Dependencies") || strings.Contains(output.String(), "\n"+result.Path+"\n") {
		t.Fatalf("JSON output contains human progress: %q", output.String())
	}
}

func TestCreateCommandDoesNotRemovePreparedWorkspaceWhenOutputFails(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "human", true: "json"}[jsonMode], func(t *testing.T) {
			workspace := &createWorkspaceStub{}
			command := createTestCommand(workspace, func(context.Context, CreateSyncRequest) (SyncCommandResult, error) {
				return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
			})
			outputErr := errors.New("output closed")
			_, err := command.Run(context.Background(), CreateCommandInput{
				Repository: createTestRepository(t), Branch: "agent/output-failure", JSON: jsonMode,
				Output: failingCreateWriter{err: outputErr},
			})
			if !errors.Is(err, outputErr) || !strings.Contains(err.Error(), "write create result") {
				t.Fatalf("error=%v, want output failure", err)
			}
			if !workspace.created || workspace.removed {
				t.Fatalf("workspace state=%#v, output failure must preserve prepared workspace", workspace)
			}
		})
	}
}

func TestCreateCommandCleansUpAfterPreparationFailureAndJoinsCleanupFailure(t *testing.T) {
	prepErr := errors.New("dependency installation failed")
	cleanupErr := errors.New("remove failed")
	workspace := &createWorkspaceStub{remove: cleanupErr}
	command := createTestCommand(workspace, func(context.Context, CreateSyncRequest) (SyncCommandResult, error) {
		return SyncCommandResult{}, prepErr
	})

	_, err := command.Run(context.Background(), CreateCommandInput{Repository: createTestRepository(t), Branch: "agent/fail"})
	if !errors.Is(err, prepErr) || !errors.Is(err, cleanupErr) || !strings.Contains(err.Error(), "cleanup also failed") {
		t.Fatalf("error=%v, want preparation and cleanup failures", err)
	}
	if !workspace.created || !workspace.removed {
		t.Fatalf("workspace cleanup state=%#v", workspace)
	}
}

func TestCreateCommandLifecycleFenceWrapsCreationAndPreparation(t *testing.T) {
	workspace := &createWorkspaceStub{}
	command := createTestCommand(workspace, func(context.Context, CreateSyncRequest) (SyncCommandResult, error) {
		return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
	})
	fenceCalled := false
	command.Fence = func(ctx context.Context, path string, operation func() error) error {
		fenceCalled = path != "" && ctx != nil
		return operation()
	}
	if _, err := command.Run(context.Background(), CreateCommandInput{Repository: createTestRepository(t), Branch: "agent/fenced"}); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if !fenceCalled {
		t.Fatal("lifecycle fence was not invoked")
	}
}
