package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/git"
)

type acquireOutputWriter struct{ err error }

func (writer acquireOutputWriter) Write([]byte) (int, error) { return 0, writer.err }

func routingRepository() git.Repository {
	return git.Repository{Root: "/repo", CommonDir: "/repo/.git", PrimaryRoot: "/repo", PrimaryCheckout: true}
}

func routingApplication(stdout *bytes.Buffer, options func(*cli.Options)) (*cli.Application, *int) {
	calls := 0
	configured := cli.Options{
		CWD:    "/repo",
		Stdout: stdout,
		Stderr: stdout,
		DiscoverRepository: func(context.Context, string) (git.Repository, error) {
			calls++
			return routingRepository(), nil
		},
		Now: func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
	}
	if options != nil {
		options(&configured)
	}
	return cli.New(configured), &calls
}

func TestApplicationRoutesInitAndSyncWithGuardPolicyAndSingleJSONOutput(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		wantGuard bool
		wantAllow bool
	}{
		{name: "init", args: []string{"init", "--json"}},
		{name: "sync override", args: []string{"sync", "--allow-shared-checkout", "--json"}, wantGuard: true, wantAllow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			guardSeen := false
			allowSeen := false
			application, discoveries := routingApplication(&stdout, func(options *cli.Options) {
				options.Sync = func(_ context.Context, input cli.SyncCommandInput) (cli.SyncCommandResult, error) {
					guardSeen, allowSeen = input.GuardSharedCheckout, input.AllowSharedCheckout
					if input.Output == nil || !input.Emit {
						t.Fatalf("sync input = %#v", input)
					}
					_, _ = fmt.Fprintln(input.Output, `{"status":"prepared"}`)
					return cli.SyncCommandResult{Status: "prepared"}, nil
				}
			})
			code, err := application.Run(context.Background(), test.args)
			if err != nil || code != 0 {
				t.Fatalf("Run = %d, %v", code, err)
			}
			if *discoveries != 1 {
				t.Fatalf("discoveries = %d, want 1", *discoveries)
			}
			if guardSeen != test.wantGuard || allowSeen != test.wantAllow {
				t.Fatalf("guard=%v allow=%v, want guard=%v allow=%v", guardSeen, allowSeen, test.wantGuard, test.wantAllow)
			}
			if strings.Count(stdout.String(), "\n") != 1 || !strings.HasPrefix(stdout.String(), `{"status":"prepared"}`) {
				t.Fatalf("stdout = %q, want one JSON record", stdout.String())
			}
		})
	}
}

func TestApplicationRoutesCreateWithStdoutWithoutDoubleWrite(t *testing.T) {
	var stdout bytes.Buffer
	application, discoveries := routingApplication(&stdout, func(options *cli.Options) {
		options.Create = func(_ context.Context, input cli.CreateCommandInput) (cli.CreateCommandResult, error) {
			if input.Repository.Root != "/repo" || input.CWD != "/repo" || input.Branch != "agent/task" ||
				input.Path != "slot" || input.From != "origin/main" || !input.Fetch || !input.Detach || !input.JSON || input.Output == nil {
				t.Fatalf("create input = %#v", input)
			}
			_, _ = fmt.Fprintln(input.Output, `{"status":"prepared"}`)
			return cli.CreateCommandResult{Status: "prepared", Path: "slot"}, nil
		}
	})
	code, err := application.Run(context.Background(), []string{"create", "agent/task", "--path", "slot", "--from", "origin/main", "--fetch", "--detach", "--json"})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if *discoveries != 1 || strings.Count(stdout.String(), "\n") != 1 {
		t.Fatalf("discoveries=%d stdout=%q, want one JSON record", *discoveries, stdout.String())
	}
}

func TestApplicationRoutesAcquireReleaseAndWritesEachOutputOnce(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		set  func(*cli.Options)
	}{
		{
			name: "acquire",
			args: []string{"acquire", "agent/task", "--ttl", "1", "--owner", "owner", "--json"},
			set: func(options *cli.Options) {
				options.Acquire = func(_ context.Context, repository git.Repository, input cli.AcquireInput) (cli.AcquireResult, error) {
					if repository.Root != "/repo" || input.Branch != "agent/task" || input.TTL != "1" || input.Owner != "owner" || !input.JSON {
						return cli.AcquireResult{}, fmt.Errorf("unexpected acquire route input: %#v", input)
					}
					return cli.AcquireResult{Output: `{"status":"assigned"}` + "\n"}, nil
				}
			},
		},
		{
			name: "release",
			args: []string{"release", "assignment-1", "--force", "--json"},
			set: func(options *cli.Options) {
				options.Release = func(_ context.Context, input cli.ReleaseInput) (cli.ReleaseResult, error) {
					if input.Repository.Root != "/repo" || input.AssignmentID != "assignment-1" || !input.Force || !input.JSON {
						return cli.ReleaseResult{}, fmt.Errorf("unexpected release route input: %#v", input)
					}
					return cli.ReleaseResult{Output: `{"status":"available"}` + "\n"}, nil
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			application, discoveries := routingApplication(&stdout, test.set)
			code, err := application.Run(context.Background(), test.args)
			if err != nil || code != 0 {
				t.Fatalf("Run = %d, %v", code, err)
			}
			if *discoveries != 1 || strings.Count(stdout.String(), "\n") != 1 {
				t.Fatalf("discoveries=%d stdout=%q, want one JSON record", *discoveries, stdout.String())
			}
		})
	}
}

func TestApplicationAcquireOutputFailureReturnsRetainedAssignmentRecovery(t *testing.T) {
	outputErr := errors.New("stdout closed")
	application, discoveries := routingApplication(nil, func(options *cli.Options) {
		options.Stdout = acquireOutputWriter{err: outputErr}
		options.Acquire = func(_ context.Context, _ git.Repository, _ cli.AcquireInput) (cli.AcquireResult, error) {
			return cli.AcquireResult{
				AcquireRecord: cli.AcquireRecord{
					AssignmentID: "assignment-opaque",
					Path:         "/repo-agent-task",
					ExpiresAt:    "2026-08-16T13:00:00Z",
				},
				Output: `{"status":"assigned"}` + "\n",
			}, nil
		}
	})

	code, err := application.Run(context.Background(), []string{"acquire", "agent/task", "--json"})
	if code != 1 || err == nil || !errors.Is(err, outputErr) {
		t.Fatalf("Run=%d, %v, want retained output failure", code, err)
	}
	var retained *cli.RetainedAssignmentError
	if !errors.As(err, &retained) {
		t.Fatalf("error=%v, want retained assignment metadata", err)
	}
	if retained.AssignmentID != "assignment-opaque" || retained.Path != "/repo-agent-task" || retained.ExpiresAt != "2026-08-16T13:00:00Z" {
		t.Fatalf("retained=%#v, want exact acquire record", retained)
	}

	var record cli.ErrorRecord
	if err := json.Unmarshal([]byte(cli.FormatJSONError(err)), &record); err != nil {
		t.Fatalf("JSON error: %v", err)
	}
	if record.Code != cli.ResourceBusyCode || !record.Retryable || record.AssignmentID != retained.AssignmentID ||
		record.Path != retained.Path || record.ExpiresAt != retained.ExpiresAt || record.Recovery != "ruk release assignment-opaque" {
		t.Fatalf("JSON record=%#v, want retained recovery metadata", record)
	}
	if *discoveries != 1 {
		t.Fatalf("discoveries=%d, want 1", *discoveries)
	}
}

func TestApplicationRoutesRemoveWithoutSuccessOutput(t *testing.T) {
	var stdout bytes.Buffer
	application, discoveries := routingApplication(&stdout, func(options *cli.Options) {
		options.Remove = func(_ context.Context, input cli.RemoveInput) error {
			if input.Repository.Root != "/repo" || input.CWD != "/repo" || input.Path != "slot" || !input.Force {
				return fmt.Errorf("unexpected remove route input: %#v", input)
			}
			return nil
		}
	})
	code, err := application.Run(context.Background(), []string{"remove", "slot", "--force"})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if *discoveries != 1 || stdout.Len() != 0 {
		t.Fatalf("discoveries=%d stdout=%q, want one discovery and no output", *discoveries, stdout.String())
	}
}
