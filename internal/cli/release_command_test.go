package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

func TestReleaseCommandFormatsHumanAndJSONSuccess(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		json   bool
		output string
	}{
		{name: "human", output: "Released /pool/repo-ruk-a\n"},
		{name: "JSON", json: true, output: `{"status":"available","assignmentId":"assignment-1","path":"/pool/repo-ruk-a","cleanedProcesses":2}` + "\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := cli.Release(context.Background(), cli.ReleaseInput{
				Repository: git.Repository{Root: "/repo"}, AssignmentID: "assignment-1", JSON: testCase.json,
			}, func(_ context.Context, repository git.Repository, assignmentID string, force bool) (cli.RepositoryReleaseResult, error) {
				if repository.Root != "/repo" || assignmentID != "assignment-1" || force {
					t.Fatalf("operation arguments = repository=%#v assignment=%q force=%t", repository, assignmentID, force)
				}
				return cli.RepositoryReleaseResult{Workspace: availableWorkspace(), CleanedProcesses: 2}, nil
			})
			if err != nil {
				t.Fatalf("Release returned an error: %v", err)
			}
			if result.Output != testCase.output {
				t.Fatalf("output = %q, want %q", result.Output, testCase.output)
			}
			if testCase.json {
				var record cli.ReleaseRecord
				if err := json.Unmarshal([]byte(result.Output), &record); err != nil {
					t.Fatalf("JSON output: %v", err)
				}
				if record.Status != "available" || record.AssignmentID != "assignment-1" || record.Path != "/pool/repo-ruk-a" {
					t.Fatalf("record = %#v", record)
				}
			}
		})
	}
}

func TestReleaseCommandPropagatesForce(t *testing.T) {
	called := false
	result, err := cli.Release(context.Background(), cli.ReleaseInput{
		Repository: git.Repository{Root: "/repo"}, AssignmentID: "assignment-1", Force: true,
	}, func(_ context.Context, _ git.Repository, _ string, force bool) (cli.RepositoryReleaseResult, error) {
		called = true
		if !force {
			t.Fatal("force option was not propagated")
		}
		return cli.RepositoryReleaseResult{Workspace: availableWorkspace()}, nil
	})
	if err != nil || !called || result.Status != "available" {
		t.Fatalf("called=%t result=%#v err=%v", called, result, err)
	}
}

func TestReleaseCommandFailureProducesNoSuccessOutput(t *testing.T) {
	want := errors.New("tracked process survived")
	result, err := cli.Release(context.Background(), cli.ReleaseInput{AssignmentID: "assignment-1", JSON: true}, func(context.Context, git.Repository, string, bool) (cli.RepositoryReleaseResult, error) {
		return cli.RepositoryReleaseResult{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if result.Output != "" {
		t.Fatalf("failure emitted success output: %q", result.Output)
	}
}

func TestReleaseCommandRejectsMalformedResult(t *testing.T) {
	for _, testCase := range []struct {
		name string
		make func() cli.RepositoryReleaseResult
		want string
	}{
		{name: "missing path", make: func() cli.RepositoryReleaseResult {
			return cli.RepositoryReleaseResult{Workspace: state.WorkspaceRecord{Lifecycle: state.LifecycleAvailable}}
		}, want: "without a path"},
		{name: "not available", make: func() cli.RepositoryReleaseResult {
			return cli.RepositoryReleaseResult{Workspace: state.WorkspaceRecord{Path: "/pool/a", Lifecycle: state.LifecycleReturning}}
		}, want: "expected available"},
		{name: "assignment retained", make: func() cli.RepositoryReleaseResult {
			return cli.RepositoryReleaseResult{Workspace: state.WorkspaceRecord{Path: "/pool/a", Lifecycle: state.LifecycleAvailable, Assignment: &state.AssignmentRecord{ID: "assignment-1"}}}
		}, want: "with an assignment"},
		{name: "negative count", make: func() cli.RepositoryReleaseResult {
			return cli.RepositoryReleaseResult{Workspace: availableWorkspace(), CleanedProcesses: -1}
		}, want: "negative cleaned process count"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := cli.Release(context.Background(), cli.ReleaseInput{AssignmentID: "assignment-1"}, func(context.Context, git.Repository, string, bool) (cli.RepositoryReleaseResult, error) {
				return testCase.make(), nil
			})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want substring %q", err, testCase.want)
			}
			if result.Output != "" {
				t.Fatalf("malformed result emitted success output: %q", result.Output)
			}
		})
	}
}

func availableWorkspace() state.WorkspaceRecord {
	return state.WorkspaceRecord{Path: "/pool/repo-ruk-a", Lifecycle: state.LifecycleAvailable, Processes: []state.TrackedProcessRecord{}}
}
