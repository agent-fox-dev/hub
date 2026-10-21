package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// tokenHub is a stub hub that records every request and answers by path.
type tokenHub struct {
	mu      sync.Mutex
	reqs    []string // "METHOD path"
	bodies  []string
	answers map[string]struct {
		status int
		body   string
	}
}

func newTokenHub(t *testing.T) (*tokenHub, *httptest.Server) {
	t.Helper()
	h := &tokenHub{answers: map[string]struct {
		status int
		body   string
	}{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.reqs = append(h.reqs, r.Method+" "+r.URL.Path)
		h.bodies = append(h.bodies, string(body))
		a, ok := h.answers[r.Method+" "+r.URL.Path]
		h.mu.Unlock()
		if !ok {
			a.status, a.body = http.StatusNotFound, `{"error":{"code":404,"message":"no stub"}}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(srv.Close)
	return h, srv
}

func (h *tokenHub) answer(method, path string, status int, body string) {
	h.answers[method+" "+path] = struct {
		status int
		body   string
	}{status, body}
}

func (h *tokenHub) count() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.reqs) }

// runTokens runs root with args against endpoint and returns stdout, stderr
// and the process exit code afc would use.
func runTokens(t *testing.T, root *cobra.Command, endpoint, apiKey string, args ...string) (string, string, int) {
	t.Helper()
	setupTestEnv(t)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	full := []string{"--endpoint-url", endpoint}
	if apiKey != "" {
		full = append(full, "--api-key", apiKey)
	}
	root.SetArgs(append(full, args...))
	err := root.Execute()
	return out.String(), errOut.String(), ExitCode(err)
}

// apikitRoot is the tree afc had before spec 34: apikit's tokens command as is.
func apikitRoot() *cobra.Command {
	root := apikit.RootCommand()
	root.AddCommand(apikit.TokensCmd())
	return root
}

const createdBody = `{"token_id":"T1","token":"ak_pat_T1_secret","name":"svc","permissions":["workspaces:read","patches:write"],"workspace":"s","expires_at":null,"created_at":"2026-10-21T10:00:00Z"}`

// TS-34-41: afc's tokens command adds --workspace to apikit's create and list,
// and wrapping a tokens command without either fails. Requirement: 34-REQ-7.1
func TestTokensCmd_Workspace_TS_34_41(t *testing.T) {
	var tokens *cobra.Command
	for _, c := range BuildRootCommand().Commands() {
		if c.Name() == "tokens" {
			tokens = c
		}
	}
	if tokens == nil {
		t.Fatal("no tokens command in the afc tree")
	}
	for _, name := range []string{"create", "list"} {
		sub, _, err := tokens.Find([]string{name})
		if err != nil || sub.Name() != name || sub.Flags().Lookup("workspace") == nil || sub.Flags().Lookup("workspace").Value.Type() != "string" {
			t.Errorf("tokens %s lacks a --workspace string flag", name)
		}
	}
	for _, missing := range []string{"create", "list"} {
		stripped := apikit.TokensCmd()
		for _, c := range stripped.Commands() {
			if c.Name() == missing {
				stripped.RemoveCommand(c)
			}
		}
		if _, err := wrapTokensCmd(stripped); err == nil {
			t.Errorf("wrapping a tokens command without %s succeeded; want an error", missing)
		}
	}
}

// TS-34-42: tokens create without --workspace behaves as apikit's command.
// Requirement: 34-REQ-7.2
func TestTokensCreate_WithoutWorkspace_TS_34_42(t *testing.T) {
	hub, srv := newTokenHub(t)
	hub.answer(http.MethodPost, "/api/v1/user/tokens", 201, `{"token_id":"T1","name":"n","token":"ak_pat_T1_s","permissions":["workspaces:read"],"expires_at":null,"created_at":"x"}`)
	args := []string{"tokens", "create", "--name", "n", "--permissions", "workspaces:read"}
	out, errOut, code := runTokens(t, BuildRootCommand(), srv.URL, "key", args...)
	wantOut, wantErr, wantCode := runTokens(t, apikitRoot(), srv.URL, "key", args...)
	if hub.reqs[0] != "POST /api/v1/user/tokens" {
		t.Errorf("request %s; want POST /api/v1/user/tokens", hub.reqs[0])
	}
	if out != wantOut || errOut != wantErr || code != wantCode || code != 0 {
		t.Errorf("(%q, %q, %d); want apikit's (%q, %q, %d)", out, errOut, code, wantOut, wantErr, wantCode)
	}
}

// TS-34-43: Invalid create flags exit 2 before any request is sent.
// Requirement: 34-REQ-7.3
func TestTokensCreate_InvalidFlags_TS_34_43(t *testing.T) {
	hub, srv := newTokenHub(t)
	for _, args := range [][]string{
		{"--workspace", "", "--name", "n", "--permissions", "p"},
		{"--workspace", "s", "--permissions", "p"},
		{"--workspace", "s", "--name", "", "--permissions", "p"},
		{"--workspace", "s", "--name", "n"},
		{"--workspace", "s", "--name", "n", "--permissions", " , ,"},
	} {
		_, _, code := runTokens(t, BuildRootCommand(), srv.URL, "key", append([]string{"tokens", "create"}, args...)...)
		if code != 2 {
			t.Errorf("%v: exit %d; want 2", args, code)
		}
	}
	if n := hub.count(); n != 0 {
		t.Errorf("hub requests = %d; want 0", n)
	}
}

// TS-34-44: tokens create --workspace posts the cleaned request, prints the
// response once and warns once on stderr. Requirement: 34-REQ-7.4
func TestTokensCreate_Workspace_TS_34_44(t *testing.T) {
	hub, srv := newTokenHub(t)
	hub.answer(http.MethodPost, "/api/v1/workspaces/s/tokens", 201, createdBody)
	out, errOut, code := runTokens(t, BuildRootCommand(), srv.URL, "key",
		"tokens", "create", "--workspace", "s", "--name", "svc", "--permissions", " workspaces:read, ,patches:write", "--expires", "30")
	if code != 0 || len(hub.reqs) != 1 || hub.reqs[0] != "POST /api/v1/workspaces/s/tokens" {
		t.Fatalf("exit %d, requests %v; want 0 and one POST /api/v1/workspaces/s/tokens", code, hub.reqs)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(hub.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	perms, _ := sent["permissions"].([]any)
	if sent["name"] != "svc" || sent["expires"] != float64(30) || len(perms) != 2 || perms[0] != "workspaces:read" || perms[1] != "patches:write" || len(sent) != 3 {
		t.Errorf("body %v; want {name: svc, permissions: [workspaces:read patches:write], expires: 30}", sent)
	}
	var printed map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&printed); err != nil || printed["token"] != "ak_pat_T1_secret" || dec.More() {
		t.Errorf("stdout %q; want one JSON object with the token", out)
	}
	lines := strings.Split(strings.TrimSpace(errOut), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "will not be shown again") {
		t.Errorf("stderr %q; want one warning line", errOut)
	}
}

// TS-34-45: A hub refusal of create exits 1 and shows the hub's message.
// Requirement: 34-REQ-7.5
func TestTokensCreate_Refused_TS_34_45(t *testing.T) {
	hub, srv := newTokenHub(t)
	hub.answer(http.MethodPost, "/api/v1/workspaces/s/tokens", 404, `{"error":{"code":404,"message":"workspace not found"}}`)
	out, errOut, code := runTokens(t, BuildRootCommand(), srv.URL, "key", "tokens", "create", "--workspace", "s", "--name", "n", "--permissions", "p")
	if code != 1 || !strings.Contains(out+errOut, "workspace not found") {
		t.Errorf("exit %d, output %q %q; want 1 and the hub's message", code, out, errOut)
	}
}

// TS-34-46: Create with no API key or an unreachable hub exits 2.
// Requirement: 34-REQ-7.6
func TestTokensCreate_NoKeyOrUnreachable_TS_34_46(t *testing.T) {
	_, srv := newTokenHub(t)
	if _, _, code := runTokens(t, BuildRootCommand(), srv.URL, "", "tokens", "create", "--workspace", "s", "--name", "n", "--permissions", "p"); code != 2 {
		t.Errorf("no API key: exit %d; want 2", code)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String()
	ln.Close()
	if _, _, code := runTokens(t, BuildRootCommand(), closed, "key", "tokens", "create", "--workspace", "s", "--name", "n", "--permissions", "p"); code != 2 {
		t.Errorf("unreachable hub: exit %d; want 2", code)
	}
}

// TS-34-47: tokens list without --workspace behaves as apikit's command.
// Requirement: 34-REQ-8.1
func TestTokensList_WithoutWorkspace_TS_34_47(t *testing.T) {
	hub, srv := newTokenHub(t)
	hub.answer(http.MethodGet, "/api/v1/user/tokens", 200, `[{"token_id":"T1","name":"n"}]`)
	out, errOut, code := runTokens(t, BuildRootCommand(), srv.URL, "key", "tokens", "list")
	wantOut, wantErr, wantCode := runTokens(t, apikitRoot(), srv.URL, "key", "tokens", "list")
	if hub.reqs[0] != "GET /api/v1/user/tokens" || out != wantOut || errOut != wantErr || code != wantCode {
		t.Errorf("request %s, (%q, %q, %d); want GET /api/v1/user/tokens and apikit's (%q, %q, %d)", hub.reqs[0], out, errOut, code, wantOut, wantErr, wantCode)
	}
}

// TS-34-48: tokens list --workspace prints the returned array without
// secrets. Requirement: 34-REQ-8.2
func TestTokensList_Workspace_TS_34_48(t *testing.T) {
	hub, srv := newTokenHub(t)
	hub.answer(http.MethodGet, "/api/v1/workspaces/s/tokens", 200,
		`[{"token_id":"T1","name":"a","permissions":[],"workspace":"s","expires_at":null,"revoked_at":null,"created_at":"x"},`+
			`{"token_id":"T2","name":"b","permissions":[],"workspace":"s","expires_at":null,"revoked_at":"y","created_at":"x"}]`)
	out, _, code := runTokens(t, BuildRootCommand(), srv.URL, "key", "tokens", "list", "--workspace", "s")
	if code != 0 || len(hub.reqs) != 1 || hub.reqs[0] != "GET /api/v1/workspaces/s/tokens" {
		t.Fatalf("exit %d, requests %v; want 0 and GET /api/v1/workspaces/s/tokens", code, hub.reqs)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(out), &arr); err != nil || len(arr) != 2 {
		t.Errorf("stdout %q; want one JSON array of two", out)
	}
	if strings.Contains(out, "secret_hash") || strings.Contains(out, `"token"`) {
		t.Errorf("stdout %q contains a secret field", out)
	}
}

// TS-34-49: tokens list with an empty --workspace exits 2 and sends nothing.
// Requirement: 34-REQ-8.3
func TestTokensList_EmptyWorkspace_TS_34_49(t *testing.T) {
	hub, srv := newTokenHub(t)
	if _, _, code := runTokens(t, BuildRootCommand(), srv.URL, "key", "tokens", "list", "--workspace", ""); code != 2 || hub.count() != 0 {
		t.Errorf("exit %d, requests %d; want 2 and 0", code, hub.count())
	}
}

// TS-34-50: A hub refusal of list exits 1 and shows the hub's message.
// Requirement: 34-REQ-8.4
func TestTokensList_Refused_TS_34_50(t *testing.T) {
	hub, srv := newTokenHub(t)
	hub.answer(http.MethodGet, "/api/v1/workspaces/s/tokens", 403, `{"error":{"code":403,"message":"this token is bound to workspace S","error_type":"token_bound"}}`)
	out, errOut, code := runTokens(t, BuildRootCommand(), srv.URL, "key", "tokens", "list", "--workspace", "s")
	if code != 1 || !strings.Contains(out+errOut, "bound to workspace") {
		t.Errorf("exit %d, output %q %q; want 1 and the hub's message", code, out, errOut)
	}
}
