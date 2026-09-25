package state_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/state"
)

const emptyMetricsJSON = `{
	"acquisitions": 0,
	"workspaceReuses": 0,
	"preparations": 0,
	"preparationSkips": 0,
	"preparationFailures": 0,
	"totalPreparationMs": 0,
	"lastPreparationMs": null
}`

func TestDecodeRejectsPreGoStateVersionsWithRecoveryGuidance(t *testing.T) {
	t.Parallel()

	for version := 1; version < state.CurrentVersion; version++ {
		input := fmt.Sprintf(`{"version":%d,"trees":{},"workspaces":{},"metrics":%s}`, version, emptyMetricsJSON)
		_, err := state.Decode([]byte(input), "state.json")
		if err == nil {
			t.Fatalf("Decode accepted version %d state", version)
		}
		want := fmt.Sprintf("uses version %d from Ruk 0.2 or earlier", version)
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "remove the file") {
			t.Fatalf("version %d error = %q, want recovery guidance", version, err)
		}
	}
}

func TestDecodeRequiresAssignmentActivityFields(t *testing.T) {
	t.Parallel()

	workspacePath := filepath.Join(t.TempDir(), "workspace")
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	input := fmt.Sprintf(`{
		"version": 4,
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
		"metrics": %s
	}`, key, workspacePath, emptyMetricsJSON)
	_, err = state.Decode([]byte(input), "state.json")
	if err == nil || !strings.Contains(err.Error(), "Unsupported or invalid Ruk state in state.json") {
		t.Fatalf("Decode error = %v, want invalid state for missing activity fields", err)
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

func TestDecodeRequiresTreeFieldsWithoutTighteningValues(t *testing.T) {
	t.Parallel()

	accepted := `{
		"version": 4,
		"workspaces": {},
		"metrics": `+emptyMetricsJSON+`,
		"trees": {
			"tree": {
				"path": "",
				"fingerprint": "",
				"mode": "",
				"projections": [],
				"branch": "",
				"updatedAt": ""
			}
		}
	}`
	if _, err := state.Decode([]byte(accepted), "state.json"); err != nil {
		t.Fatalf("Decode rejected historically tolerated string values: %v", err)
	}

	missingPath := `{
		"version": 4,
		"workspaces": {},
		"metrics": `+emptyMetricsJSON+`,
		"trees": {
			"tree": {
				"fingerprint": "",
				"mode": "",
				"projections": [],
				"branch": "",
				"updatedAt": ""
			}
		}
	}`
	_, err := state.Decode([]byte(missingPath), "state.json")
	if err == nil {
		t.Fatal("Decode accepted a tree with a missing path field")
	}
	if !strings.Contains(err.Error(), "Unsupported or invalid Ruk state in state.json") {
		t.Fatalf("error = %q, want invalid-state classification", err)
	}
}
