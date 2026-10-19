package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/txsvc/apikit"
)

// sandboxProfilesBody is what a hub with one profile answers.
const sandboxProfilesBody = `{"profiles":[{"name":"agent","default":true,"image":"registry.example/agent:1","network":"none","ttl_seconds":1200,"resources":{"cpus":2,"memory_mb":4096,"pids":512,"disk_mb":0},"secrets":["GIT_PAT"],"optional_secrets":[],"env_keys":["LANG"],"require_landlock":true}]}`

// sandboxHub is a stub hub that answers every request with one fixed response
// and records what it was asked.
type sandboxHub struct {
	*httptest.Server

	mu      sync.Mutex
	methods []string
	paths   []string
	auths   []string
}

func newSandboxHub(t *testing.T, status int, body string) *sandboxHub {
	t.Helper()
	hub := &sandboxHub{}
	hub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.mu.Lock()
		hub.methods = append(hub.methods, r.Method)
		hub.paths = append(hub.paths, r.URL.Path)
		hub.auths = append(hub.auths, r.Header.Get("Authorization"))
		hub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(hub.Close)
	return hub
}

func (h *sandboxHub) requests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.paths)
}

// decodeOneDocument decodes stdout, which must hold exactly one JSON document.
func decodeOneDocument(t *testing.T, stdout string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%q", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout holds more than one JSON document: %q", stdout)
	}
	return doc
}

// TS-25-49: afc sandbox profiles prints the response as one JSON document.
// Requirement: 25-REQ-9.1
func TestSandboxProfilesCmd_PrintsResponse_TS_25_49(t *testing.T) {
	setupTestEnv(t)
	hub := newSandboxHub(t, http.StatusOK, sandboxProfilesBody)

	stdout, _, err := runRootCmd(t, "--endpoint-url", hub.URL, "--api-key", "test-api-key", "sandbox", "profiles")
	if err != nil {
		t.Fatalf("afc sandbox profiles returned error: %v", err)
	}
	if code := ExitCode(err); code != 0 {
		t.Errorf("exit code = %d; want 0", code)
	}

	var want any
	if err := json.Unmarshal([]byte(sandboxProfilesBody), &want); err != nil {
		t.Fatalf("test body is not JSON: %v", err)
	}
	if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout = %v; want the hub's response %v", got, want)
	}

	if !reflect.DeepEqual(hub.methods, []string{http.MethodGet}) ||
		!reflect.DeepEqual(hub.paths, []string{"/api/v1/sandbox-profiles"}) ||
		!reflect.DeepEqual(hub.auths, []string{"Bearer test-api-key"}) {
		t.Errorf("requests = %v %v with credentials %v; want one GET /api/v1/sandbox-profiles with the configured API key",
			hub.methods, hub.paths, hub.auths)
	}
}

// TS-25-50: afc sandbox profiles rejects positional arguments without sending
// a request.
// Requirement: 25-REQ-9.2
func TestSandboxProfilesCmd_RejectsArguments_TS_25_50(t *testing.T) {
	setupTestEnv(t)
	hub := newSandboxHub(t, http.StatusOK, sandboxProfilesBody)

	stdout, _, err := runRootCmd(t, "--endpoint-url", hub.URL, "--api-key", "test-api-key", "sandbox", "profiles", "extra")
	if err == nil {
		t.Fatal("afc sandbox profiles accepted a positional argument")
	}
	if !strings.Contains(err.Error(), "extra") {
		t.Errorf("error %q does not name the rejected argument", err)
	}
	if code := ExitCode(err); code != 2 {
		t.Errorf("exit code = %d; want 2 for a usage error", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q; want nothing", stdout)
	}
	if n := hub.requests(); n != 0 {
		t.Errorf("the hub saw %d requests; want none", n)
	}
}

// TS-25-51: Error statuses from the hub are reported through CLIHandleError:
// apikit prints an error envelope with the hub's code and message on stdout,
// nothing on stderr, and the exit code is apikit's mapping of an API error.
// apikit's client keeps the code and the message of an error response, not
// its error_type, so the disabled 404 is recognised by its message.
// Requirement: 25-REQ-9.3
func TestSandboxProfilesCmd_ReportsHubErrors_TS_25_51(t *testing.T) {
	for _, tc := range []struct {
		status  int
		message string
	}{
		{http.StatusNotFound, "sandboxes are not enabled on this hub"},
		{http.StatusUnauthorized, "authentication required"},
		{http.StatusForbidden, "PAT requires sandboxes:read scope"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			setupTestEnv(t)
			errorBody := map[string]any{"code": tc.status, "message": tc.message}
			if tc.status == http.StatusNotFound {
				errorBody["error_type"] = "sandboxes_disabled"
			}
			raw, _ := json.Marshal(map[string]any{"error": errorBody})
			hub := newSandboxHub(t, tc.status, string(raw))

			stdout, stderr, err := runRootCmd(t, "--endpoint-url", hub.URL, "--api-key", "test-api-key", "sandbox", "profiles")
			if err == nil {
				t.Fatal("afc sandbox profiles succeeded against an error response")
			}
			if code := ExitCode(err); code == 0 || code != apikit.CLIExitCode(err) {
				t.Errorf("exit code = %d; want apikit's non-zero mapping %d", code, apikit.CLIExitCode(err))
			}
			if stderr != "" {
				t.Errorf("stderr = %q; want nothing", stderr)
			}

			envelope, ok := decodeOneDocument(t, stdout).(map[string]any)["error"].(map[string]any)
			if !ok {
				t.Fatalf("stdout is not an error envelope: %q", stdout)
			}
			if envelope["code"] != float64(tc.status) || envelope["message"] != tc.message {
				t.Errorf("envelope = %v; want code %d and the hub's message %q", envelope, tc.status, tc.message)
			}
		})
	}
}

// TS-25-52: BuildRootCommand registers the sandbox group, whose only
// subcommand is profiles.
// Requirement: 25-REQ-9.4
func TestBuildRootCommand_SandboxGroup_TS_25_52(t *testing.T) {
	sandbox, _, err := BuildRootCommand().Find([]string{"sandbox"})
	if err != nil || sandbox.Name() != "sandbox" {
		t.Fatalf("root command has no sandbox command: %v", err)
	}
	var names []string
	for _, sub := range sandbox.Commands() {
		names = append(names, sub.Name())
	}
	if !reflect.DeepEqual(names, []string{"profiles"}) {
		t.Errorf("sandbox subcommands = %v; want [profiles]", names)
	}
}
