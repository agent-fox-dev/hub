package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// forgeHub is a stub hub that answers every request with one fixed response and
// records the method, the escaped path and the raw query of each.
type forgeHub struct {
	*httptest.Server

	mu       sync.Mutex
	requests []forgeRequest
}

// forgeRequest is one request the stub hub received.
type forgeRequest struct {
	Method     string
	Path       string // escaped
	RawQuery   string
	RequestURI string
}

func newForgeHub(t *testing.T, status int, body string) *forgeHub {
	t.Helper()
	hub := &forgeHub{}
	hub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.mu.Lock()
		hub.requests = append(hub.requests, forgeRequest{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.RequestURI})
		hub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(hub.Close)
	return hub
}

func (h *forgeHub) received() []forgeRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]forgeRequest(nil), h.requests...)
}

// runAfc runs afc against the hub and returns stdout and the error.
func (h *forgeHub) runAfc(t *testing.T, args ...string) (string, error) {
	t.Helper()
	setupTestEnv(t)
	stdout, _, err := runRootCmd(t, append([]string{"--endpoint-url", h.URL, "--api-key", "test-api-key"}, args...)...)
	return stdout, err
}

// onlyRequest returns the one request the hub received.
func (h *forgeHub) onlyRequest(t *testing.T) forgeRequest {
	t.Helper()
	reqs := h.received()
	if len(reqs) != 1 {
		t.Fatalf("the hub received %d requests; want 1: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

const forgeStatusBody = `{"forge":"github","api_url":"https://api.github.com","repository":{"host":"github.com","owner":"acme","name":"widgets"},"credential":"pat","upstream":null}`

// TS-30-48: afc forge status prints one JSON document from GET /forge.
// Requirement: 30-REQ-10.1
func TestForgeStatusCmd_TS_30_48(t *testing.T) {
	hub := newForgeHub(t, http.StatusOK, forgeStatusBody)

	stdout, err := hub.runAfc(t, "forge", "status", "widgets")
	if err != nil || ExitCode(err) != 0 {
		t.Fatalf("afc forge status: %v (exit %d)", err, ExitCode(err))
	}
	req := hub.onlyRequest(t)
	if req.Method != http.MethodGet || req.Path != "/api/v1/workspaces/widgets/forge" || req.RawQuery != "" {
		t.Errorf("the hub received %+v; want GET /api/v1/workspaces/widgets/forge", req)
	}
	var want any
	if err := json.Unmarshal([]byte(forgeStatusBody), &want); err != nil {
		t.Fatal(err)
	}
	if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout = %v; want %v", got, want)
	}
}

// TS-30-49: afc issue list maps flags to query parameters, joining labels and omitting
// unset flags.
// Requirement: 30-REQ-10.2
func TestIssueListCmd_TS_30_49(t *testing.T) {
	hub := newForgeHub(t, http.StatusOK, `[]`)

	stdout, err := hub.runAfc(t, "issue", "list", "widgets",
		"--label", "a", "--label", "b", "--state", "open", "--assignee", "bob", "--limit", "5", "--target", "upstream")
	if err != nil {
		t.Fatalf("afc issue list: %v", err)
	}
	decodeOneDocument(t, stdout)
	req := hub.onlyRequest(t)
	if req.Path != "/api/v1/workspaces/widgets/issues" {
		t.Errorf("path = %s; want /api/v1/workspaces/widgets/issues", req.Path)
	}
	query, err := url.ParseQuery(req.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{"labels": {"a,b"}, "state": {"open"}, "assignee": {"bob"}, "limit": {"5"}, "target": {"upstream"}}
	if !reflect.DeepEqual(query, want) {
		t.Errorf("query = %v; want %v", query, want)
	}

	hub2 := newForgeHub(t, http.StatusOK, `[]`)
	stdout, err = hub2.runAfc(t, "issue", "list", "widgets")
	if err != nil {
		t.Fatalf("afc issue list: %v", err)
	}
	decodeOneDocument(t, stdout)
	if req := hub2.onlyRequest(t); strings.Contains(req.RequestURI, "?") {
		t.Errorf("without flags the request is %+v; want no query string", req)
	}
}

// The values are sent as given, whatever they are: the hub validates them.
// Requirement: 30-REQ-10.2
func TestIssueListCmdSendsValuesAsGiven(t *testing.T) {
	hub := newForgeHub(t, http.StatusBadRequest, `{"error":{"code":400,"message":"limit must be an integer from 1 to 100"}}`)

	_, err := hub.runAfc(t, "issue", "list", "widgets", "--limit", "abc", "--state", "", "--label", "a,b", "--label", "c d", "--target", "ORIGIN")
	if ExitCode(err) != 1 {
		t.Errorf("exit code = %d; want 1 for the hub's refusal", ExitCode(err))
	}
	query, perr := url.ParseQuery(hub.onlyRequest(t).RawQuery)
	if perr != nil {
		t.Fatal(perr)
	}
	want := url.Values{"limit": {"abc"}, "state": {""}, "labels": {"a,b,c d"}, "target": {"ORIGIN"}}
	if !reflect.DeepEqual(query, want) {
		t.Errorf("query = %v; want %v", query, want)
	}

	// A flag that is set to nothing is still set.
	hub = newForgeHub(t, http.StatusBadRequest, `{"error":{"code":400,"message":"limit must be an integer from 1 to 100"}}`)
	hub.runAfc(t, "issue", "list", "widgets", "--limit", "", "--assignee", "", "--target", "")
	query, perr = url.ParseQuery(hub.onlyRequest(t).RawQuery)
	if perr != nil {
		t.Fatal(perr)
	}
	want = url.Values{"limit": {""}, "assignee": {""}, "target": {""}}
	if !reflect.DeepEqual(query, want) {
		t.Errorf("query = %v; want %v", query, want)
	}
}

// TS-30-50: afc issue get and pr get|state|files|checks|reviews hit the right paths, with
// the target only when set.
// Requirement: 30-REQ-10.3
func TestNumberedCmds_TS_30_50(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		suffix string
	}{
		{[]string{"issue", "get"}, "/issues/42"},
		{[]string{"pr", "get"}, "/pulls/42"},
		{[]string{"pr", "state"}, "/pulls/42/state"},
		{[]string{"pr", "files"}, "/pulls/42/files"},
		{[]string{"pr", "checks"}, "/pulls/42/checks"},
		{[]string{"pr", "reviews"}, "/pulls/42/reviews"},
	} {
		name := strings.Join(tc.args, " ")
		t.Run(name, func(t *testing.T) {
			for _, slug := range []string{"w", "needs/escaping ?#"} {
				hub := newForgeHub(t, http.StatusOK, `{"number":42}`)
				stdout, err := hub.runAfc(t, append(append([]string{}, tc.args...), slug, "42")...)
				if err != nil || ExitCode(err) != 0 {
					t.Fatalf("afc %s: %v", name, err)
				}
				decodeOneDocument(t, stdout)
				req := hub.onlyRequest(t)
				if want := "/api/v1" + apiPath("workspaces", slug) + tc.suffix; req.Path != want || req.RawQuery != "" {
					t.Errorf("slug %q: the hub received %+v; want %s with no query", slug, req, want)
				}

				hub = newForgeHub(t, http.StatusOK, `{"number":42}`)
				if _, err := hub.runAfc(t, append(append([]string{}, tc.args...), slug, "42", "--target", "upstream")...); err != nil {
					t.Fatalf("afc %s --target: %v", name, err)
				}
				if req := hub.onlyRequest(t); req.RawQuery != "target=upstream" {
					t.Errorf("slug %q with --target: query = %q; want target=upstream", slug, req.RawQuery)
				}
			}
		})
	}
}

// The number is escaped like every path segment and is sent as given.
// Requirement: 30-REQ-10.3
func TestNumberIsEscapedAndSentAsGiven(t *testing.T) {
	hub := newForgeHub(t, http.StatusBadRequest, `{"error":{"code":400,"message":"n must be a positive decimal integer"}}`)
	_, err := hub.runAfc(t, "pr", "state", "widgets", "1/../2")
	if ExitCode(err) != 1 {
		t.Errorf("exit code = %d; want 1", ExitCode(err))
	}
	if req := hub.onlyRequest(t); req.Path != "/api/v1/workspaces/widgets/pulls/1%2F..%2F2/state" {
		t.Errorf("path = %s; want the number escaped as one segment", req.Path)
	}
}

// TS-30-51: A hub error status is returned through CLIHandleError with apikit's exit code.
// Requirement: 30-REQ-10.4
func TestHubErrorsExitWithAPIKitsCode_TS_30_51(t *testing.T) {
	for status, errorType := range map[int]string{404: "forge_not_found", 422: "forge_rejected", 429: "forge_rate_limited"} {
		body := fmt.Sprintf(`{"error":{"code":%d,"message":"the forge says no","error_type":%q}}`, status, errorType)
		t.Run(errorType, func(t *testing.T) {
			hub := newForgeHub(t, status, body)
			stdout, err := hub.runAfc(t, "issue", "get", "widgets", "7")
			if err == nil {
				t.Fatal("afc issue get succeeded on an error status")
			}
			if code := ExitCode(err); code == 0 || code != 1 {
				t.Errorf("exit code = %d; want apikit's 1 for an API error", code)
			}
			if !hasErrorEnvelope(stdout) {
				t.Errorf("stdout holds no error envelope: %q", stdout)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil || doc["error"] == nil || len(doc) != 1 {
				t.Errorf("stdout is %q; want the error envelope alone, no result document", stdout)
			}
		})
	}
}

// TS-30-52: Wrong positional argument counts fail without contacting hub.
// Requirement: 30-REQ-10.5
func TestForgeCmdsWithWrongArgumentCounts_TS_30_52(t *testing.T) {
	for _, args := range [][]string{
		{"forge", "status"},
		{"forge", "status", "w", "extra"},
		{"issue", "list"},
		{"issue", "list", "w", "extra"},
		{"issue", "get"},
		{"issue", "get", "w"},
		{"issue", "get", "w", "1", "extra"},
		{"pr", "get"},
		{"pr", "state", "w"},
		{"pr", "files", "w"},
		{"pr", "checks", "w"},
		{"pr", "reviews", "w"},
		{"pr", "get", "w", "1", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			hub := newForgeHub(t, http.StatusOK, `{}`)
			_, err := hub.runAfc(t, args...)
			if err == nil || ExitCode(err) == 0 {
				t.Errorf("afc %v succeeded (exit %d)", args, ExitCode(err))
			}
			if n := len(hub.received()); n != 0 {
				t.Errorf("the hub received %d requests; want none", n)
			}
		})
	}
}

// The same commands with the right number of arguments do contact the hub, so that the
// test above is not satisfied by a command that does not exist.
// Requirement: 30-REQ-10.5
func TestForgeCmdsWithTheRightArgumentCountsContactTheHub(t *testing.T) {
	for _, args := range [][]string{
		{"forge", "status", "w"}, {"issue", "list", "w"}, {"issue", "get", "w", "1"},
		{"pr", "get", "w", "1"}, {"pr", "state", "w", "1"}, {"pr", "files", "w", "1"},
		{"pr", "checks", "w", "1"}, {"pr", "reviews", "w", "1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			hub := newForgeHub(t, http.StatusOK, `{}`)
			if _, err := hub.runAfc(t, args...); err != nil {
				t.Fatalf("afc %v: %v", args, err)
			}
			hub.onlyRequest(t)
		})
	}
}

// The commands are on the root command, and afc stays free of the forge client.
// Requirement: 30-REQ-10.1, 30-REQ-1.3
func TestForgeCmdsAreRegistered(t *testing.T) {
	root := BuildRootCommand()
	for _, path := range [][]string{
		{"forge", "status"}, {"issue", "list"}, {"issue", "get"},
		{"pr", "get"}, {"pr", "state"}, {"pr", "files"}, {"pr", "checks"}, {"pr", "reviews"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil || cmd == nil || cmd.Name() != path[len(path)-1] {
			t.Errorf("afc %v is not a command: %v", path, err)
		}
	}
}

// The slug is escaped like every path segment in the commands that take no number.
// Requirement: 30-REQ-10.1, 30-REQ-10.2
func TestForgeStatusAndIssueListEscapeTheSlug(t *testing.T) {
	const slug = "needs/escaping ?#"
	for _, tc := range []struct {
		args   []string
		suffix string
	}{
		{[]string{"forge", "status", slug}, "/forge"},
		{[]string{"issue", "list", slug}, "/issues"},
	} {
		hub := newForgeHub(t, http.StatusOK, `[]`)
		if _, err := hub.runAfc(t, tc.args...); err != nil {
			t.Fatalf("afc %v: %v", tc.args, err)
		}
		if want := "/api/v1" + apiPath("workspaces", slug) + tc.suffix; hub.onlyRequest(t).Path != want {
			t.Errorf("afc %v: path = %s; want %s", tc.args, hub.onlyRequest(t).Path, want)
		}
	}
}
