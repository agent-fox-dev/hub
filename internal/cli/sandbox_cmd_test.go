package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// TS-25-52: BuildRootCommand registers the sandbox group with the profiles
// subcommand. Spec 26 adds the lease subcommands, so the group's subcommands are
// now create, destroy, get, list and profiles; TestSandboxGroup_LeaseCommands_
// TS_26_55 checks that they and nothing else are there.
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
	if !slices.Contains(names, "profiles") {
		t.Errorf("sandbox subcommands = %v; want profiles among them", names)
	}
}

// ---------------------------------------------------------------------------
// Spec 26: afc sandbox create, list, get and destroy
// ---------------------------------------------------------------------------

// leaseToken and leaseBody are what a hub answers to a successful create.
const (
	leaseTokenText = "sbl_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	leaseIDText    = "sbx_0123456789abcdef0123456789abcdef"
)

var leaseBodyText = `{"id":"` + leaseIDText + `","workspace":"widgets","profile":"tools","purpose":"","state":"leased","expires_at":"2030-01-02T04:04:05Z","image":"img","network":"none","resources":{"cpus":0,"memory_mb":0,"pids":0,"disk_mb":0},"require_landlock":false,"secrets":[],"optional_secrets":[],"labels":{},"created_by":"u1","created_at":"2030-01-02T03:04:05Z","updated_at":"2030-01-02T03:04:05Z","sandbox_id":null,"error":null,"ended_at":null,"end_reason":null}`

// leaseHubRequest is one request a stub hub received.
type leaseHubRequest struct {
	Method, Path, Query, Auth, Body string
}

// leaseHub is a stub hub that records every request in full and answers
// through handler.
type leaseHub struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []leaseHubRequest
}

func newLeaseHub(t *testing.T, handler func(w http.ResponseWriter, r leaseHubRequest)) *leaseHub {
	t.Helper()
	hub := &leaseHub{}
	hub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := leaseHubRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(raw)}
		hub.mu.Lock()
		hub.reqs = append(hub.reqs, rec)
		hub.mu.Unlock()
		handler(w, rec)
	}))
	t.Cleanup(hub.Close)
	return hub
}

func (h *leaseHub) requests() []leaseHubRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.reqs)
}

// respond writes a JSON answer.
func respond(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// createdHub answers a POST with a 201 lease that carries the token, and a
// DELETE with a 204.
func createdHub(t *testing.T) *leaseHub {
	return newLeaseHub(t, func(w http.ResponseWriter, r leaseHubRequest) {
		switch r.Method {
		case http.MethodPost:
			respond(w, http.StatusCreated, strings.TrimSuffix(leaseBodyText, "}")+`,"token":"`+leaseTokenText+`"}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			respond(w, http.StatusOK, leaseBodyText)
		}
	})
}

// runLease runs afc against hub.
func runLease(t *testing.T, hub *leaseHub, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	full := append([]string{"--endpoint-url", hub.URL, "--api-key", "test-api-key"}, args...)
	return runRootCmd(t, full...)
}

// postedBody decodes the body of the one POST a hub received.
func postedBody(t *testing.T, hub *leaseHub) map[string]any {
	t.Helper()
	reqs := hub.requests()
	if len(reqs) == 0 || reqs[0].Method != http.MethodPost {
		t.Fatalf("requests = %+v; want a POST first", reqs)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil {
		t.Fatalf("the POST body is not a JSON object: %v\n%s", err, reqs[0].Body)
	}
	return body
}

// TS-26-55: afc sandbox create --token-file writes the token to a 0600 file and
// leaves it out of stdout.
// Requirement: 26-REQ-3.1
func TestSandboxCreate_TokenFile_TS_26_55(t *testing.T) {
	hub := createdHub(t)
	path := filepath.Join(t.TempDir(), "token")
	// A file that exists, with wider permissions and old content, is truncated
	// and narrowed.
	if err := os.WriteFile(path, []byte("old content that is longer than the token, for sure, yes"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runLease(t, hub, "sandbox", "create", "widgets", "--profile", "tools", "--label", "k=v", "--label", "a=b=c", "--token-file", path)
	if err != nil {
		t.Fatalf("afc sandbox create returned error: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the token file is missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v; want 0600", info.Mode().Perm())
	}
	if got, _ := os.ReadFile(path); string(got) != leaseTokenText {
		t.Errorf("token file holds %q; want exactly the token with no trailing newline", got)
	}

	doc, ok := decodeOneDocument(t, stdout).(map[string]any)
	if !ok || doc["id"] != leaseIDText {
		t.Fatalf("stdout is not the lease: %q", stdout)
	}
	if _, has := doc["token"]; has {
		t.Errorf("stdout has a token key: %q", stdout)
	}
	if strings.Contains(stdout+stderr, leaseTokenText) {
		t.Errorf("the token reached stdout or stderr:\nstdout: %s\nstderr: %s", stdout, stderr)
	}

	reqs := hub.requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodPost || reqs[0].Path != "/api/v1/workspaces/widgets/sandboxes" || reqs[0].Auth != "Bearer test-api-key" {
		t.Fatalf("requests = %+v; want one authenticated POST /api/v1/workspaces/widgets/sandboxes", reqs)
	}
	body := postedBody(t, hub)
	labels, _ := body["labels"].(map[string]any)
	if body["profile"] != "tools" || len(labels) != 2 || labels["k"] != "v" || labels["a"] != "b=c" {
		t.Errorf("POST body = %v; want profile tools and labels {k: v, a: b=c}", body)
	}
	for _, key := range []string{"purpose", "ttl"} {
		if _, has := body[key]; has {
			t.Errorf("POST body has %s although no flag set it: %v", key, body)
		}
	}
}

// Without --token-file the whole lease, token included, is printed, and stderr
// warns that the token will not be shown again. Flags that are set reach the
// server, and --ttl is passed on unparsed.
// Requirement: 26-REQ-10 (the create command)
func TestSandboxCreate_PrintsTokenAndWarns(t *testing.T) {
	hub := createdHub(t)
	stdout, stderr, err := runLease(t, hub, "sandbox", "create", "widgets", "--purpose", "triage", "--ttl", "soon")
	if err != nil {
		t.Fatalf("afc sandbox create returned error: %v", err)
	}
	doc, _ := decodeOneDocument(t, stdout).(map[string]any)
	if doc["token"] != leaseTokenText || doc["state"] != "leased" {
		t.Errorf("stdout = %q; want the lease with its token", stdout)
	}
	if !strings.Contains(stderr, "not be shown again") || strings.Contains(stderr, leaseTokenText) {
		t.Errorf("stderr = %q; want a warning that the token will not be shown again, without the token", stderr)
	}
	body := postedBody(t, hub)
	if body["purpose"] != "triage" || body["ttl"] != "soon" {
		t.Errorf("POST body = %v; want purpose triage and ttl soon, unparsed", body)
	}
	if _, has := body["profile"]; has {
		t.Errorf("POST body has a profile although --profile was not set: %v", body)
	}
}

// A label without '=', a repeated label key and a token file that cannot be
// opened all exit 2 before any request is sent.
// Requirement: 26-REQ-10 (the create command)
func TestSandboxCreate_RefusedBeforeAnyRequest(t *testing.T) {
	for name, args := range map[string][]string{
		"label without =":      {"--label", "novalue"},
		"repeated label key":   {"--label", "k=1", "--label", "k=2"},
		"token file in a void": {"--token-file", filepath.Join(t.TempDir(), "no", "such", "dir", "token")},
		"token file is a dir":  {"--token-file", t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			hub := createdHub(t)
			stdout, _, err := runLease(t, hub, append([]string{"sandbox", "create", "widgets"}, args...)...)
			if err == nil {
				t.Fatalf("afc sandbox create accepted the invocation; stdout: %s", stdout)
			}
			if code := ExitCode(err); code != 2 {
				t.Errorf("exit code = %d; want 2", code)
			}
			if n := len(hub.requests()); n != 0 {
				t.Errorf("the hub saw %d requests; want none", n)
			}
		})
	}
}

// If the token cannot be written after the lease was issued, afc releases the
// lease with a best-effort DELETE and exits non-zero naming the file. The token
// never falls back to stdout.
// Requirement: 26-REQ-10 (the create command)
func TestSandboxCreate_FailedTokenWriteRollsBack(t *testing.T) {
	prev := writeTokenFile
	writeTokenFile = func(*os.File, string) error { return errors.New("disk full") }
	t.Cleanup(func() { writeTokenFile = prev })

	hub := createdHub(t)
	path := filepath.Join(t.TempDir(), "token")
	stdout, stderr, err := runLease(t, hub, "sandbox", "create", "widgets", "--token-file", path)
	if err == nil || ExitCode(err) == 0 {
		t.Fatalf("afc sandbox create succeeded although the token could not be written (exit %d)", ExitCode(err))
	}

	reqs := hub.requests()
	if len(reqs) != 2 || reqs[1].Method != http.MethodDelete || reqs[1].Path != "/api/v1/workspaces/widgets/sandboxes/"+leaseIDText {
		t.Fatalf("requests = %+v; want the POST and then a DELETE of the new lease", reqs)
	}
	if out := stdout + stderr + err.Error(); !strings.Contains(out, path) {
		t.Errorf("output does not name the token file %s:\n%s", path, out)
	}
	if strings.Contains(stdout+stderr, leaseTokenText) {
		t.Errorf("the token reached stdout or stderr after the failed write:\n%s\n%s", stdout, stderr)
	}
}

// When the hub refuses the create, afc exits non-zero, writes no token and
// reports the refusal.
// Requirement: 26-REQ-10 (the create command)
func TestSandboxCreate_HubRefusal(t *testing.T) {
	hub := newLeaseHub(t, func(w http.ResponseWriter, r leaseHubRequest) {
		respond(w, http.StatusUnprocessableEntity, `{"error":{"code":422,"message":"required secrets not found: GIT_PAT (searched scopes: workspace, user)","error_type":"secret_missing"}}`)
	})
	path := filepath.Join(t.TempDir(), "token")
	stdout, _, err := runLease(t, hub, "sandbox", "create", "widgets", "--token-file", path)
	if err == nil || ExitCode(err) != 1 {
		t.Fatalf("exit code = %d (%v); want 1", ExitCode(err), err)
	}
	if !strings.Contains(stdout, "GIT_PAT") {
		t.Errorf("stdout = %q; want the hub's message", stdout)
	}
	if got, _ := os.ReadFile(path); len(got) != 0 {
		t.Errorf("the token file holds %q after a refused create", got)
	}
}

// The group has the lease subcommands and nothing unexpected.
// Requirement: 26-REQ-10
func TestSandboxGroup_LeaseCommands_TS_26_55(t *testing.T) {
	sandbox, _, err := BuildRootCommand().Find([]string{"sandbox"})
	if err != nil {
		t.Fatalf("no sandbox command: %v", err)
	}
	var names []string
	for _, sub := range sandbox.Commands() {
		names = append(names, sub.Name())
	}
	slices.Sort(names)
	if want := []string{"create", "destroy", "get", "list", "profiles"}; !slices.Equal(names, want) {
		t.Errorf("sandbox subcommands = %v; want %v", names, want)
	}
}

// afc sandbox list and get send the reads and print what the hub answers.
// Requirement: 26-REQ-10
func TestSandboxListAndGet(t *testing.T) {
	hub := newLeaseHub(t, func(w http.ResponseWriter, r leaseHubRequest) {
		if strings.HasSuffix(r.Path, leaseIDText) {
			respond(w, http.StatusOK, leaseBodyText)
			return
		}
		respond(w, http.StatusOK, "["+leaseBodyText+"]")
	})

	stdout, _, err := runLease(t, hub, "sandbox", "list", "widgets")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list, ok := decodeOneDocument(t, stdout).([]any); !ok || len(list) != 1 {
		t.Errorf("list printed %q; want a one-element array", stdout)
	}
	if _, _, err := runLease(t, hub, "sandbox", "list", "widgets", "--all"); err != nil {
		t.Fatalf("list --all: %v", err)
	}
	stdout, _, err = runLease(t, hub, "sandbox", "get", "widgets", leaseIDText)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if doc, ok := decodeOneDocument(t, stdout).(map[string]any); !ok || doc["id"] != leaseIDText {
		t.Errorf("get printed %q; want the lease", stdout)
	}

	reqs := hub.requests()
	want := []leaseHubRequest{
		{http.MethodGet, "/api/v1/workspaces/widgets/sandboxes", "", "Bearer test-api-key", ""},
		{http.MethodGet, "/api/v1/workspaces/widgets/sandboxes", "all=true", "Bearer test-api-key", ""},
		{http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/" + leaseIDText, "", "Bearer test-api-key", ""},
	}
	if !reflect.DeepEqual(reqs, want) {
		t.Errorf("requests = %+v\nwant       %+v", reqs, want)
	}

	// Arguments are checked before any request.
	before := len(hub.requests())
	for _, args := range [][]string{{"list"}, {"list", "a", "b"}, {"get", "widgets"}, {"get", "a", "b", "c"}, {"create"}, {"destroy", "widgets"}} {
		_, _, err := runLease(t, hub, append([]string{"sandbox"}, args...)...)
		if err == nil || ExitCode(err) != 2 {
			t.Errorf("afc sandbox %v: error %v, exit %d; want a usage error with exit 2", args, err, ExitCode(err))
		}
	}
	if len(hub.requests()) != before {
		t.Error("a usage error sent a request")
	}
}

// TS-26-56: afc sandbox destroy sends DELETE and prints the synthetic status
// document, with a confirmation on stderr.
// Requirement: 26-REQ-8.1
func TestSandboxDestroy_TS_26_56(t *testing.T) {
	hub := newLeaseHub(t, func(w http.ResponseWriter, r leaseHubRequest) { w.WriteHeader(http.StatusNoContent) })

	stdout, stderr, err := runLease(t, hub, "sandbox", "destroy", "widgets", "sbx_x")
	if err != nil {
		t.Fatalf("afc sandbox destroy returned error: %v", err)
	}
	want := map[string]any{"status": "destroyed", "id": "sbx_x"}
	if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout = %v; want %v", got, want)
	}
	if !strings.Contains(stderr, "sbx_x") {
		t.Errorf("stderr = %q; want a confirmation naming the lease", stderr)
	}
	reqs := hub.requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodDelete || reqs[0].Path != "/api/v1/workspaces/widgets/sandboxes/sbx_x" {
		t.Errorf("requests = %+v; want one DELETE /api/v1/workspaces/widgets/sandboxes/sbx_x", reqs)
	}
}

// A hub error on destroy is reported through apikit with its exit code and no
// status document.
// Requirement: 26-REQ-8.1
func TestSandboxDestroy_HubError(t *testing.T) {
	hub := newLeaseHub(t, func(w http.ResponseWriter, r leaseHubRequest) {
		respond(w, http.StatusNotFound, `{"error":{"code":404,"message":"sandbox lease not found"}}`)
	})
	stdout, _, err := runLease(t, hub, "sandbox", "destroy", "widgets", "sbx_x")
	if err == nil || ExitCode(err) != 1 {
		t.Fatalf("exit code = %d (%v); want 1", ExitCode(err), err)
	}
	if strings.Contains(stdout, `"destroyed"`) || !strings.Contains(stdout, "sandbox lease not found") {
		t.Errorf("stdout = %q; want the hub's error and no status document", stdout)
	}
}
