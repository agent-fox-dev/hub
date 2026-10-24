package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// buildTestRoot is a root command with apikit's persistent flags and
// credential resolution, the module's commands and a workspace parent for
// patch-status: the shape afc gives them.
func buildTestRoot() *cobra.Command {
	root := apikit.RootCommand()
	root.Use = "afc"
	workspace := &cobra.Command{Use: "workspace", Short: "Manage workspaces"}
	workspace.AddCommand(PatchStatusCmd())
	root.AddCommand(PatchCmd(), RebuildCmd(), RerereCmd(), workspace)
	return root
}

// errorResp is the JSON error envelope of the mock API.
type errorResp struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// setupTestEnv isolates tests from the user's real config directory by
// pointing $HOME at a temp dir and creating a minimal config.toml.
func setupTestEnv(t *testing.T) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	configDir := filepath.Join(tmpHome, ".ak")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("endpoint_url = \"\"\nuser_id = \"\"\napi_key = \"\"\n"), 0600); err != nil {
		t.Fatalf("failed to write config.toml: %v", err)
	}
}

// runWorkspaceCmd executes a workspace subcommand through the root command
// tree and captures stdout/stderr.
func runWorkspaceCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := buildTestRoot()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	fullArgs := append([]string{"--endpoint-url", baseURL, "--api-key", apiKey, "workspace"}, args...)
	root.SetArgs(fullArgs)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// hasErrorEnvelope returns true if stdout contains a JSON error envelope.
func hasErrorEnvelope(stdout string) bool {
	dec := json.NewDecoder(strings.NewReader(stdout))
	for {
		var raw any
		if err := dec.Decode(&raw); err != nil {
			break
		}
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := obj["error"].(map[string]any); ok {
			return true
		}
	}
	return false
}
