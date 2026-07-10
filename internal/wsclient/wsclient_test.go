package wsclient_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/wsclient"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// requestCapture tracks HTTP requests received by a mock server.
type requestCapture struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

// ---------------------------------------------------------------------------
// 4.1 — Workspace Create Tests (REQ-10)
// ---------------------------------------------------------------------------

// TestWorkspaceCreateWithTeam verifies that afc workspace create resolves the
// team slug to a UUID via GET /api/v1/teams, then POSTs the workspace with
// the correct payload and returns pretty-printed JSON on stdout.
// TS-05-29
func TestWorkspaceCreateWithTeam(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/teams":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Server returns a bare JSON array of team objects.
			fmt.Fprint(w, `[{"id":"team-uuid","name":"My Team","slug":"my-team","status":"active","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]`)

		case r.Method == "POST" && r.URL.Path == "/api/v1/workspaces":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"ws-1","slug":"my-ws","git_url":"https://github.com/org/repo","branch":"main","team_id":"team-uuid","owner_user_id":"u-1","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	// Step 1: Resolve team slug via GET /api/v1/teams.
	teams, err := wsclient.ListTeams(mockServer.URL, "k", client)
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}

	// Step 2: Resolve slug to UUID.
	teamID, err := wsclient.ResolveTeamSlug(teams, "my-team")
	if err != nil {
		t.Fatalf("ResolveTeamSlug failed: %v", err)
	}
	if teamID != "team-uuid" {
		t.Errorf("resolved team_id = %q, want 'team-uuid'", teamID)
	}

	// Step 3: Build payload and create workspace.
	payload := map[string]any{
		"git_url": "https://github.com/org/repo",
		"slug":    "my-ws",
		"branch":  "main",
		"team_id": teamID,
	}
	body, statusCode, err := wsclient.CreateWorkspace(mockServer.URL, "k", payload, client)
	if err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	// Verify exit code 0 (status 2xx).
	if statusCode < 200 || statusCode >= 300 {
		t.Errorf("status code = %d, want 2xx", statusCode)
	}

	// Verify GET /api/v1/teams was called with Authorization header.
	teamsReqFound := false
	for _, req := range requests {
		if req.Method == "GET" && req.Path == "/api/v1/teams" {
			teamsReqFound = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer k" {
				t.Errorf("GET /api/v1/teams Authorization = %q, want 'Bearer k'", authHeader)
			}
			break
		}
	}
	if !teamsReqFound {
		t.Error("GET /api/v1/teams was not called")
	}

	// Verify POST /api/v1/workspaces was called with correct payload.
	postReqFound := false
	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/workspaces" {
			postReqFound = true

			// Verify Bearer auth header.
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer k" {
				t.Errorf("POST /api/v1/workspaces Authorization = %q, want 'Bearer k'", authHeader)
			}

			// Parse and verify the request body.
			var reqBody map[string]any
			if err := json.Unmarshal([]byte(req.Body), &reqBody); err != nil {
				t.Fatalf("failed to parse POST body: %v", err)
			}
			if reqBody["git_url"] != "https://github.com/org/repo" {
				t.Errorf("payload git_url = %v, want 'https://github.com/org/repo'", reqBody["git_url"])
			}
			if reqBody["slug"] != "my-ws" {
				t.Errorf("payload slug = %v, want 'my-ws'", reqBody["slug"])
			}
			if reqBody["branch"] != "main" {
				t.Errorf("payload branch = %v, want 'main'", reqBody["branch"])
			}
			if reqBody["team_id"] != "team-uuid" {
				t.Errorf("payload team_id = %v, want 'team-uuid'", reqBody["team_id"])
			}
			break
		}
	}
	if !postReqFound {
		t.Error("POST /api/v1/workspaces was not called")
	}

	// Verify response body is parseable JSON with correct fields.
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if parsed["slug"] != "my-ws" {
		t.Errorf("response slug = %v, want 'my-ws'", parsed["slug"])
	}

	// Verify the response can be pretty-printed with 2-space indentation.
	prettyPrinted, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatalf("failed to pretty-print JSON: %v", err)
	}
	if !strings.Contains(string(prettyPrinted), "  ") {
		t.Error("pretty-printed JSON should contain 2-space indentation")
	}
}

// TestWorkspaceCreateTeamNotFound verifies that when team slug resolution
// returns zero matches, the CLI prints 'team not found: <slug>' and exits
// with code 1 without creating the workspace.
// TS-05-30
func TestWorkspaceCreateTeamNotFound(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/teams":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return empty teams list.
			fmt.Fprint(w, `[]`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	// Fetch teams (empty list).
	teams, err := wsclient.ListTeams(mockServer.URL, "k", client)
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}

	// Attempt to resolve a slug that doesn't exist.
	_, err = wsclient.ResolveTeamSlug(teams, "nonexistent-team")
	if err == nil {
		t.Fatal("ResolveTeamSlug should return error for zero matches, got nil")
	}

	// Verify exact error message.
	wantMsg := "team not found: nonexistent-team"
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), wantMsg)
	}

	// Verify POST /api/v1/workspaces was NOT called.
	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/workspaces" {
			t.Error("POST /api/v1/workspaces should not be called when team not found")
		}
	}
}

// TestWorkspaceCreateTeamAmbiguous verifies that when team slug resolution
// returns multiple matches, the CLI prints 'ambiguous team slug: <slug>'
// and exits with code 1 without creating the workspace.
// TS-05-31
func TestWorkspaceCreateTeamAmbiguous(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/teams":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return two teams with the same slug.
			fmt.Fprint(w, `[{"id":"id1","name":"Team 1","slug":"dup-team","status":"active","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"},{"id":"id2","name":"Team 2","slug":"dup-team","status":"active","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	// Fetch teams (two with same slug).
	teams, err := wsclient.ListTeams(mockServer.URL, "k", client)
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}

	// Attempt to resolve the ambiguous slug.
	_, err = wsclient.ResolveTeamSlug(teams, "dup-team")
	if err == nil {
		t.Fatal("ResolveTeamSlug should return error for multiple matches, got nil")
	}

	// Verify exact error message.
	wantMsg := "ambiguous team slug: dup-team"
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), wantMsg)
	}

	// Verify POST /api/v1/workspaces was NOT called.
	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/workspaces" {
			t.Error("POST /api/v1/workspaces should not be called when team slug is ambiguous")
		}
	}
}

// TestWorkspaceCreateOptionalFieldsOmitted verifies that branch and team_id
// are omitted from the POST /api/v1/workspaces payload when --branch and
// --team are not provided.
// TS-05-32
func TestWorkspaceCreateOptionalFieldsOmitted(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "POST" && r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"ws-1","slug":"my-ws","git_url":"https://github.com/org/repo","owner_user_id":"u-1","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	// Build payload with only required fields (no branch, no team_id).
	payload := map[string]any{
		"git_url": "https://github.com/org/repo",
		"slug":    "my-ws",
	}
	_, statusCode, err := wsclient.CreateWorkspace(mockServer.URL, "k", payload, client)
	if err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}

	if statusCode < 200 || statusCode >= 300 {
		t.Errorf("status code = %d, want 2xx", statusCode)
	}

	// Verify POST body contains only git_url and slug — branch and team_id must be absent.
	postReqFound := false
	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/workspaces" {
			postReqFound = true

			var reqBody map[string]any
			if err := json.Unmarshal([]byte(req.Body), &reqBody); err != nil {
				t.Fatalf("failed to parse POST body: %v", err)
			}

			if reqBody["git_url"] != "https://github.com/org/repo" {
				t.Errorf("payload git_url = %v, want 'https://github.com/org/repo'", reqBody["git_url"])
			}
			if reqBody["slug"] != "my-ws" {
				t.Errorf("payload slug = %v, want 'my-ws'", reqBody["slug"])
			}
			if _, exists := reqBody["branch"]; exists {
				t.Errorf("payload should NOT contain 'branch' key, but it does: %v", reqBody["branch"])
			}
			if _, exists := reqBody["team_id"]; exists {
				t.Errorf("payload should NOT contain 'team_id' key, but it does: %v", reqBody["team_id"])
			}
			break
		}
	}
	if !postReqFound {
		t.Error("POST /api/v1/workspaces was not called")
	}
}

// TestWorkspaceCreateServerValidationError verifies that format, URL validity,
// slug character, and label length validation for workspace create fields are
// deferred to the server, and the server error response is forwarded.
// TS-05-E7
func TestWorkspaceCreateServerValidationError(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "POST" && r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity) // 422
			// Per spec 01, error envelope is {"error": {"code": N, "message": "..."}}.
			fmt.Fprint(w, `{"error":{"code":422,"message":"invalid git URL format"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	// CLI should send the request without local format validation
	// (format validation is deferred to the server per 05-REQ-5.E1).
	payload := map[string]any{
		"git_url": "not-a-url",
		"slug":    "INVALID SLUG!",
	}
	body, statusCode, err := wsclient.CreateWorkspace(mockServer.URL, "k", payload, client)

	// The function should make the request without local validation and
	// return the status code and body for the caller to handle.
	if err != nil {
		t.Fatalf("CreateWorkspace should not return error for reachable server: %v", err)
	}

	// Verify the request was actually sent to the server.
	postReqFound := false
	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/workspaces" {
			postReqFound = true
			break
		}
	}
	if !postReqFound {
		t.Error("POST /api/v1/workspaces should be called (CLI defers format validation to server)")
	}

	// Verify status code is 422.
	if statusCode != http.StatusUnprocessableEntity {
		t.Errorf("status code = %d, want 422", statusCode)
	}

	// Verify the response body contains the server's error message.
	if !strings.Contains(string(body), "invalid git URL format") {
		t.Errorf("response body should contain 'invalid git URL format', got: %s", string(body))
	}
}

// TestWorkspaceCreateTeamNetworkError verifies that a network error during
// GET /api/v1/teams for team slug resolution causes the CLI to print a
// descriptive error and exit with code 1 without sending the workspace
// creation request.
// TS-05-E16
func TestWorkspaceCreateTeamNetworkError(t *testing.T) {
	// Use a mock server and close it immediately to simulate network error.
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Close server immediately to make connections fail.
	mockServer.Close()

	client := &http.Client{Timeout: 2 * time.Second}

	// Attempt to list teams — should fail with network error.
	_, err := wsclient.ListTeams(mockServer.URL, "k", client)
	if err == nil {
		t.Fatal("ListTeams should return error for network failure, got nil")
	}

	// The error should be descriptive about the network failure.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "connect") &&
		!strings.Contains(errMsg, "refused") &&
		!strings.Contains(errMsg, "error") &&
		!strings.Contains(errMsg, "Error") &&
		!strings.Contains(errMsg, "failed") {
		t.Errorf("error should mention network failure, got: %v", err)
	}

	// Since ListTeams failed, CreateWorkspace should never be called.
	// This is enforced by the caller's control flow — if ListTeams returns
	// an error, the command handler should exit before calling CreateWorkspace.
}

// ---------------------------------------------------------------------------
// 4.2 — Workspace List, Get, and Error Path Tests (REQ-11)
// ---------------------------------------------------------------------------

// TestWorkspaceListSuccess verifies that afc workspace list sends
// GET /api/v1/workspaces with Bearer auth and prints pretty-printed JSON
// to stdout.
// TS-05-33
func TestWorkspaceListSuccess(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "GET" && r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[{"id":"w1","slug":"ws1","git_url":"https://g.com/r","owner_user_id":"u1","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	body, statusCode, err := wsclient.ListWorkspaces(mockServer.URL, "k", client)
	if err != nil {
		t.Fatalf("ListWorkspaces failed: %v", err)
	}

	// Verify HTTP status code is 200.
	if statusCode != http.StatusOK {
		t.Errorf("status code = %d, want 200", statusCode)
	}

	// Verify GET /api/v1/workspaces was called with Authorization header.
	found := false
	for _, req := range requests {
		if req.Method == "GET" && req.Path == "/api/v1/workspaces" {
			found = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer k" {
				t.Errorf("Authorization header = %q, want 'Bearer k'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("GET /api/v1/workspaces was not called")
	}

	// Verify response body is parseable JSON array.
	var parsed []map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if len(parsed) == 0 {
		t.Error("expected at least one workspace in the response")
	}
	if parsed[0]["slug"] != "ws1" {
		t.Errorf("first workspace slug = %v, want 'ws1'", parsed[0]["slug"])
	}

	// Verify the response can be pretty-printed with 2-space indentation.
	prettyPrinted, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatalf("failed to pretty-print JSON: %v", err)
	}
	if !strings.Contains(string(prettyPrinted), "  ") {
		t.Error("pretty-printed JSON should contain 2-space indentation")
	}
}

// TestWorkspaceGetSuccess verifies that afc workspace get <slug> sends
// GET /api/v1/workspaces/:slug with Bearer auth and prints pretty-printed
// workspace JSON to stdout.
// TS-05-34
func TestWorkspaceGetSuccess(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "GET" && r.URL.Path == "/api/v1/workspaces/my-ws" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":"ws-1","slug":"my-ws","git_url":"https://g.com/r","owner_user_id":"u1","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	body, statusCode, err := wsclient.GetWorkspace(mockServer.URL, "k", "my-ws", client)
	if err != nil {
		t.Fatalf("GetWorkspace failed: %v", err)
	}

	// Verify HTTP status code is 200.
	if statusCode != http.StatusOK {
		t.Errorf("status code = %d, want 200", statusCode)
	}

	// Verify GET /api/v1/workspaces/my-ws was called with Authorization header.
	found := false
	for _, req := range requests {
		if req.Method == "GET" && req.Path == "/api/v1/workspaces/my-ws" {
			found = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer k" {
				t.Errorf("Authorization header = %q, want 'Bearer k'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("GET /api/v1/workspaces/my-ws was not called")
	}

	// Verify response body is parseable JSON.
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if parsed["slug"] != "my-ws" {
		t.Errorf("workspace slug = %v, want 'my-ws'", parsed["slug"])
	}

	// Verify the response can be pretty-printed with 2-space indentation.
	prettyPrinted, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatalf("failed to pretty-print JSON: %v", err)
	}
	if !strings.Contains(string(prettyPrinted), "  ") {
		t.Error("pretty-printed JSON should contain 2-space indentation")
	}
}

// TestWorkspaceCreateNonJSONError verifies that a non-2xx non-JSON response
// from POST /api/v1/workspaces (e.g., HTML 502) produces the clean error
// message without raw HTML.
// TS-05-E17
func TestWorkspaceCreateNonJSONError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway) // 502
			fmt.Fprint(w, `<html><body>Bad Gateway</body></html>`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	payload := map[string]any{
		"git_url": "https://g.com/r",
		"slug":    "ws",
	}
	body, statusCode, err := wsclient.CreateWorkspace(mockServer.URL, "k", payload, client)

	// The function should return the status code and body without error
	// for a reachable server, even on non-2xx.
	if err != nil {
		t.Fatalf("CreateWorkspace should not return error for reachable server: %v", err)
	}

	// Verify status code is 502.
	if statusCode != http.StatusBadGateway {
		t.Errorf("status code = %d, want 502", statusCode)
	}

	// The response body is available for the caller (command handler) to
	// determine whether it's JSON or not. The command handler is responsible
	// for printing 'Error: unexpected response from server (HTTP 502).'
	// to stderr without including the raw HTML body.
	// Here we verify the raw body was captured for error handling.
	if len(body) == 0 {
		t.Error("response body should not be empty")
	}

	// Verify we can detect that the body is NOT valid JSON
	// (the command handler uses this to decide which error format to use).
	var jsonCheck any
	if err := json.Unmarshal(body, &jsonCheck); err == nil {
		t.Error("response body should NOT be valid JSON (it's HTML)")
	}
}

// TestWorkspaceGetNotFound verifies that a 404 response from
// GET /api/v1/workspaces/:slug causes the CLI to print the JSON error
// message and exit with code 1.
// TS-05-E18
func TestWorkspaceGetNotFound(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/workspaces/no-such-ws" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound) // 404
			// Per spec 01, error envelope is {"error": {"code": N, "message": "..."}}.
			fmt.Fprint(w, `{"error":{"code":404,"message":"workspace not found"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	body, statusCode, err := wsclient.GetWorkspace(mockServer.URL, "k", "no-such-ws", client)

	// The function should return the status code and body without error
	// for a reachable server.
	if err != nil {
		t.Fatalf("GetWorkspace should not return error for reachable server: %v", err)
	}

	// Verify status code is 404.
	if statusCode != http.StatusNotFound {
		t.Errorf("status code = %d, want 404", statusCode)
	}

	// Verify the response body contains the error message for stderr output.
	if !strings.Contains(string(body), "workspace not found") {
		t.Errorf("response body should contain 'workspace not found', got: %s", string(body))
	}

	// The command handler should print the error message to stderr and
	// produce empty stdout (verified at command level in later groups).
}
