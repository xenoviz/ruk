package state_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/state"
)

func TestDecodeMigratesVersionOneState(t *testing.T) {
	t.Parallel()

	decoded, err := state.Decode([]byte(`{
		"version": 1,
		"trees": {
			"legacy": {
				"path": "/tmp/ruk-workspace",
				"fingerprint": "fingerprint",
				"mode": "managed-install",
				"projections": ["node_modules"],
				"branch": "agent/test",
				"updatedAt": "1970-01-01T00:00:00.000Z"
			}
		}
	}`), "state.json")
	if err != nil {
		t.Fatalf("Decode returned an error: %v", err)
	}
	if decoded.Version != state.CurrentVersion {
		t.Fatalf("Version = %d, want %d", decoded.Version, state.CurrentVersion)
	}
	if len(decoded.Trees) != 1 {
		t.Fatalf("Trees has %d records, want 1", len(decoded.Trees))
	}
	if decoded.Workspaces == nil || len(decoded.Workspaces) != 0 {
		t.Fatalf("Workspaces = %#v, want an empty non-nil map", decoded.Workspaces)
	}
	if got, want := decoded.Metrics, state.EmptyMetrics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Metrics = %#v, want %#v", got, want)
	}
}

func TestDecodeMigratesVersionThreeActivity(t *testing.T) {
	t.Parallel()

	workspacePath := filepath.Join(t.TempDir(), "workspace")
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	input := fmt.Sprintf(`{
		"version": 3,
		"trees": {},
		"workspaces": {
			%q: {
				"path": %q,
				"managed": true,
				"branch": "agent/test",
				"lifecycle": "assigned",
				"operationId": null,
				"assignment": {
					"id": "46bc4998-95b0-4d16-b017-69b06a13747b",
					"owner": "agent",
					"hostname": "host",
					"assignedAt": "2026-01-01T00:00:00.000Z",
					"renewedAt": "2026-01-01T01:00:00.000Z",
					"expiresAt": "2026-01-01T03:00:00.000Z",
					"ports": {}
				},
				"processes": [],
				"createdAt": "2026-01-01T00:00:00.000Z",
				"updatedAt": "2026-01-01T01:00:00.000Z",
				"availableAt": null,
				"failure": null
			}
		},
		"metrics": {
			"acquisitions": 0,
			"workspaceReuses": 0,
			"preparations": 0,
			"preparationSkips": 0,
			"preparationFailures": 0,
			"totalPreparationMs": 0,
			"lastPreparationMs": null
		}
	}`, key, workspacePath)

	decoded, err := state.Decode([]byte(input), "state.json")
	if err != nil {
		t.Fatalf("Decode returned an error: %v", err)
	}
	assignment := decoded.Workspaces[key].Assignment
	if assignment == nil {
		t.Fatal("Assignment is nil")
	}
	if assignment.LeaseDurationMinutes != 120 {
		t.Fatalf("LeaseDurationMinutes = %v, want 120", assignment.LeaseDurationMinutes)
	}
	if assignment.LastActivityAt != "2026-01-01T01:00:00.000Z" {
		t.Fatalf("LastActivityAt = %q, want renewal timestamp", assignment.LastActivityAt)
	}
	if assignment.LeaseKeepers == nil || len(assignment.LeaseKeepers) != 0 {
		t.Fatalf("LeaseKeepers = %#v, want an empty non-nil slice", assignment.LeaseKeepers)
	}
}

func TestDecodeRejectsMalformedAndUnsupportedState(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		input       string
		wantMessage string
	}{
		"malformed": {
			input:       "not-json",
			wantMessage: "Cannot parse Ruk state in state.json",
		},
		"unsupported": {
			input:       `{"version":99,"trees":{}}`,
			wantMessage: "Unsupported or invalid Ruk state in state.json",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := state.Decode([]byte(testCase.input), "state.json")
			if err == nil {
				t.Fatal("Decode returned nil error")
			}
			if !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("error = %q, want it to contain %q", err, testCase.wantMessage)
			}
		})
	}
}
