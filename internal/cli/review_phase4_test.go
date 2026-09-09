package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression tests for the CLI findings of the 2026-09 codebase review.

// runAfc executes the root command with the given stdin and arguments.
func runAfc(t *testing.T, baseURL, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"--endpoint-url", baseURL, "--api-key", "test-api-key"}, args...))
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// recordedRequest captures what the mock hub received.
type recordedRequest struct {
	Method string
	Path   string // escaped path as sent on the wire
	Body   map[string]any
}

// recordingServer answers every request with the given status and body and
// records the requests it received.
func recordingServer(t *testing.T, status int, body any) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var parsed map[string]any
		if data, _ := io.ReadAll(r.Body); len(data) > 0 {
			_ = json.Unmarshal(data, &parsed)
		}
		mu.Lock()
		reqs = append(reqs, recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Body: parsed})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), reqs...)
	}
}

func TestCLI_PathSegmentsArePercentEncoded(t *testing.T) {
	srv, requests := recordingServer(t, http.StatusOK, map[string]any{"ok": true})

	if _, _, err := runAfc(t, srv.URL, "", "secrets", "delete", "a/b c", "--org", "my org"); err != nil {
		t.Fatalf("secrets delete: %v", err)
	}
	if _, _, err := runAfc(t, srv.URL, "", "patch", "restore", "ws/../other", "id 1"); err != nil {
		t.Fatalf("patch restore: %v", err)
	}
	if _, _, err := runAfc(t, srv.URL, "", "rerere", "forget", "ws", "src/a b.txt"); err != nil {
		t.Fatalf("rerere forget: %v", err)
	}

	got := requests()
	want := []string{
		"/api/v1/orgs/my%20org/secrets/a%2Fb%20c",
		"/api/v1/workspaces/ws%2F..%2Fother/patches/id%201/restore",
		"/api/v1/workspaces/ws/rerere/src/a%20b.txt",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d requests, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Path != want[i] {
			t.Errorf("request %d path = %q, want %q", i, got[i].Path, want[i])
		}
	}
}

func TestCLI_SecretsCreate_MultipleArgsAndStdin(t *testing.T) {
	srv, requests := recordingServer(t, http.StatusCreated, map[string]any{"created": 2})

	if _, _, err := runAfc(t, srv.URL, "", "secrets", "create", "A=1", "B=2,C=3"); err != nil {
		t.Fatalf("multiple args: %v", err)
	}
	if _, _, err := runAfc(t, srv.URL, "s3cr3t\n", "secrets", "create", "TOKEN", "--from-stdin"); err != nil {
		t.Fatalf("from stdin: %v", err)
	}
	if _, _, err := runAfc(t, srv.URL, "new\r\n", "secrets", "update", "TOKEN", "--from-stdin"); err != nil {
		t.Fatalf("update from stdin: %v", err)
	}
	if _, _, err := runAfc(t, srv.URL, "x", "secrets", "create", "A=1", "--from-stdin"); err == nil {
		t.Error("--from-stdin with KEY=VALUE should be rejected")
	}

	got := requests()
	if len(got) != 3 {
		t.Fatalf("got %d requests, want 3", len(got))
	}
	keys := func(body map[string]any) []string {
		var out []string
		for _, e := range body["entries"].([]any) {
			m := e.(map[string]any)
			out = append(out, m["key"].(string)+"="+m["value"].(string))
		}
		return out
	}
	if k := keys(got[0].Body); strings.Join(k, " ") != "A=1 B=2 C=3" {
		t.Errorf("entries = %v, want A=1 B=2 C=3", k)
	}
	if k := keys(got[1].Body); strings.Join(k, " ") != "TOKEN=s3cr3t" {
		t.Errorf("stdin entries = %v, want TOKEN=s3cr3t", k)
	}
	if got[2].Path != "/api/v1/user/secrets/TOKEN" || got[2].Body["value"] != "new" {
		t.Errorf("update request = %+v, want value 'new' at /user/secrets/TOKEN", got[2])
	}
}

func TestCLI_CredentialSet_RequiresUsernamePasswordPairAndReadsStdin(t *testing.T) {
	srv, requests := recordingServer(t, http.StatusCreated, map[string]any{"created": 1})

	stdout, _, err := runAfc(t, srv.URL, "", "credential", "set", "ws", "--upstream-git-username", "bob")
	if err == nil {
		t.Fatal("username without password should be rejected")
	}
	if !strings.Contains(stdout, "must be provided together") {
		t.Errorf("error envelope missing pairing message: %s", stdout)
	}
	if n := len(requests()); n != 0 {
		t.Fatalf("server received %d requests, want 0", n)
	}

	if _, _, err := runAfc(t, srv.URL, "ghp_token\n", "credential", "set", "ws", "--from-stdin"); err != nil {
		t.Fatalf("PAT from stdin: %v", err)
	}
	if _, _, err := runAfc(t, srv.URL, "pw\n", "credential", "set", "ws", "--upstream-git-username", "bob", "--from-stdin"); err != nil {
		t.Fatalf("password from stdin: %v", err)
	}
	got := requests()
	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2", len(got))
	}
	entries := func(body map[string]any) map[string]string {
		out := map[string]string{}
		for _, e := range body["entries"].([]any) {
			m := e.(map[string]any)
			out[m["key"].(string)] = m["value"].(string)
		}
		return out
	}
	if e := entries(got[0].Body); e["UPSTREAM_GIT_PAT"] != "ghp_token" || len(e) != 1 {
		t.Errorf("PAT entries = %v", e)
	}
	if e := entries(got[1].Body); e["UPSTREAM_GIT_USERNAME"] != "bob" || e["UPSTREAM_GIT_PASSWORD"] != "pw" {
		t.Errorf("basic auth entries = %v", e)
	}
}

func TestCLI_PatchUpdate_RequiresAChange(t *testing.T) {
	srv, requests := recordingServer(t, http.StatusOK, map[string]any{"id": "p1"})
	stdout, _, err := runAfc(t, srv.URL, "", "patch", "update", "ws", "p1")
	if err == nil {
		t.Fatal("patch update without flags should be rejected")
	}
	if !strings.Contains(stdout, "at least one of") {
		t.Errorf("error envelope = %s", stdout)
	}
	if n := len(requests()); n != 0 {
		t.Errorf("server received %d requests, want 0", n)
	}
}

// jobServer serves a job record whose status changes after the given number
// of GET requests.
func jobServer(t *testing.T, statuses ...string) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		idx := polls
		polls++
		mu.Unlock()
		if idx >= len(statuses) {
			idx = len(statuses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "job-1", "status": statuses[idx]})
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return polls }
}

func TestCLI_WaitSubcommands_PollImmediatelyAndReportStatus(t *testing.T) {
	// The job is already complete: with an immediate first poll the command
	// returns well before the (deliberately long) poll interval elapses.
	srv, polls := jobServer(t, "completed")
	start := time.Now()
	stdout, _, err := runAfc(t, srv.URL, "", "rebuild", "wait", "ws", "job-1", "--poll-interval", "30s")
	if err != nil {
		t.Fatalf("rebuild wait: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("rebuild wait took %s; the first poll must not wait for the interval", time.Since(start))
	}
	if polls() != 1 || !strings.Contains(stdout, "completed") {
		t.Errorf("polls = %d stdout = %s", polls(), stdout)
	}

	// Running -> dead_letter: exits non-zero after printing the record.
	srv2, _ := jobServer(t, "running", "dead_letter")
	stdout, stderr, err := runAfc(t, srv2.URL, "", "merge", "wait", "ws", "job-1", "--poll-interval", "50ms")
	if err == nil {
		t.Fatal("merge wait on a dead_letter job should exit non-zero")
	}
	if !strings.Contains(stdout, `"dead_letter"`) || !strings.Contains(stderr, "dead_letter") {
		t.Errorf("stdout = %s stderr = %s", stdout, stderr)
	}
}

func TestCLI_WorkspaceCreate_WaitFailedCloneExitsNonZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := "cloning"
		if r.Method == http.MethodGet {
			status = "failed"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"slug": "ws", "clone_status": status})
	}))
	t.Cleanup(srv.Close)
	stdout, _, err := runAfc(t, srv.URL, "", "workspace", "create", "--slug", "ws", "--git-url", "https://example.com/r.git",
		"--wait", "--poll-interval", "50ms")
	if err == nil {
		t.Fatal("failed clone should exit non-zero")
	}
	if !strings.Contains(stdout, "workspace clone failed") {
		t.Errorf("stdout = %s", stdout)
	}
}
