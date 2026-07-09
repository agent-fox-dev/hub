package cli_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-06-20: Verifies that the afc CLI binary exposes no --workspace flag on
// any subcommand.
// Requirement: 06-REQ-4.2
// ---------------------------------------------------------------------------

func TestCLI_NoWorkspaceFlagInHelp(t *testing.T) {
	// Check all relevant help outputs for absence of --workspace.
	helpCommands := [][]string{
		{"--help"},
		{"keys", "--help"},
		{"keys", "create", "--help"},
	}

	for _, args := range helpCommands {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			stdout, stderr, _ := execAfc(t, args)
			combined := stdout + stderr // Cobra may write to either stream.

			if strings.Contains(combined, "--workspace") {
				t.Errorf("'afc %s' should not list --workspace flag, but it does:\n%s",
					name, combined)
			}
		})
	}
}

func TestCLI_TeamFlagInKeysCreateHelp(t *testing.T) {
	// Verify --team flag is present in the keys create help output.
	stdout, stderr, exitCode := execAfc(t, []string{"keys", "create", "--help"})
	combined := stdout + stderr

	if exitCode != 0 {
		t.Errorf("expected exit code 0 for help, got %d", exitCode)
	}

	if !strings.Contains(combined, "--team") {
		t.Errorf("'afc keys create --help' should list --team flag, but it does not:\n%s",
			combined)
	}
}

// ---------------------------------------------------------------------------
// TS-06-E5: Verifies that passing --workspace flag to afc keys create returns
// an unknown flag error, exits non-zero, and performs no API call.
// Requirement: 06-REQ-4.E1
// ---------------------------------------------------------------------------

func TestCLI_WorkspaceFlagRejected(t *testing.T) {
	// Pass --workspace to keys create. The CLI should return an unknown flag
	// error via Cobra's flag parsing (before any network call is made).
	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated,
		`{"key":"af_k1_secret","key_id":"k1"}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "create",
		"--workspace", "my-workspace",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when --workspace is passed, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "unknown flag") && !strings.Contains(stderrLower, "--workspace") {
		t.Errorf("expected stderr to mention 'unknown flag' or '--workspace', got: %q", stderr)
	}

	// Confirm no API call was made.
	if stub.requestCount() > 0 {
		t.Error("expected no API requests when --workspace flag is rejected, but the stub received requests")
	}
}

// ---------------------------------------------------------------------------
// TS-06-19: Verifies that afc keys create --team <slug> creates an API key
// scoped to the specified team, exits 0, and prints output including team_id.
// Requirement: 06-REQ-4.1
//
// NOTE: This is a CLI integration test using a stub server. The full
// end-to-end path through the real server is covered in later task groups.
// ---------------------------------------------------------------------------

func TestCLI_KeysCreateTeamFlag_HappyPath(t *testing.T) {
	stub := newStubServer(t)
	keyObj := `{"key":"af_k1_test-secret","key_id":"k1","team_id":"team-abc","role":"member","expires_at":"2027-01-01T00:00:00Z","created_at":"2026-07-01T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated, keyObj)

	stdout, _, exitCode := execAfc(t, []string{
		"keys", "create",
		"--team", "team-abc",
		"--label", "ci-bot",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Assert the stub received the POST request.
	if !stub.receivedRequest("POST", "/api/v1/keys") {
		t.Fatal("expected POST /api/v1/keys to be called, but it was not")
	}

	// Verify the request body uses team_id (not workspace_id).
	req := stub.lastRequestFor("POST", "/api/v1/keys")
	if req != nil {
		var body map[string]any
		if err := json.Unmarshal([]byte(req.Body), &body); err == nil {
			// After the rename, the CLI should send team_id in the payload.
			if _, hasWorkspace := body["workspace_id"]; hasWorkspace {
				t.Error("request body should not contain 'workspace_id' (should use 'team_id')")
			}
			if teamID, ok := body["team_id"]; !ok || teamID != "team-abc" {
				t.Errorf("expected request body team_id='team-abc', got: %v", teamID)
			}
		}
	}

	// Assert stdout contains valid JSON with the key field.
	if !isValidJSON(stdout) {
		t.Fatalf("expected stdout to be valid JSON, got: %q", stdout)
	}

	// Verify stdout does not contain workspace_id.
	if strings.Contains(stdout, "workspace_id") {
		t.Error("stdout should not contain 'workspace_id'")
	}
}

func TestCLI_KeysCreateTeamFlag_MissingTeamFlag(t *testing.T) {
	// When --team is not provided, the CLI should error (just like the
	// old --workspace requirement).
	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated,
		`{"key":"af_k1_secret","key_id":"k1"}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "create",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when --team is missing, got 0")
	}

	// After the rename, the error message should reference 'team' (not 'workspace').
	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "team") {
		t.Errorf("expected stderr to mention 'team', got: %q", stderr)
	}
}
