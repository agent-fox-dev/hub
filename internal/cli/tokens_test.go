package cli

import (
	"bytes"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// runTokens runs a tokens command on root against the hub and returns what
// it printed and the exit code.
func runTokens(t *testing.T, root *cobra.Command, h *hub, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	isolateHome(t)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--endpoint-url", h.URL, "--api-key", testAPIKey}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), ExitCode(err)
}

// apikitRoot is apikit's tokens command as it is, without the hub's --workspace.
func apikitRoot() *cobra.Command {
	root := apikit.RootCommand()
	root.AddCommand(apikit.TokensCmd())
	return root
}

// Without --workspace, create and list behave exactly as apikit's commands.
func TestTokens_WithoutWorkspaceBehaveAsAPIKits(t *testing.T) {
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"tokens", "create", "--name", "n", "--permissions", "workspaces:read"}, "POST /api/v1/user/tokens"},
		{[]string{"tokens", "list"}, "GET /api/v1/user/tokens"},
	} {
		h := newHub(t, http.StatusOK, `{"token_id":"T1","name":"n","token":"ak_pat_T1_s","permissions":["workspaces:read"]}`)
		out, errOut, code := runTokens(t, BuildRootCommand(), h, tc.args...)
		wantOut, wantErr, wantCode := runTokens(t, apikitRoot(), h, tc.args...)
		reqs := h.requests()
		if len(reqs) != 2 || reqs[0].Method+" "+reqs[0].Path != tc.path || reqs[1] != reqs[0] {
			t.Errorf("%v: requests = %+v; want two of %s", tc.args, reqs, tc.path)
		}
		if out != wantOut || errOut != wantErr || code != wantCode || code != 0 {
			t.Errorf("%v: (%q, %q, %d); want apikit's (%q, %q, %d)", tc.args, out, errOut, code, wantOut, wantErr, wantCode)
		}
	}
}

// create --workspace posts the name, the trimmed non-empty permissions and
// the expiry to the workspace's tokens, prints the answer once and warns once.
func TestTokensCreate_WorkspaceMintsABoundToken(t *testing.T) {
	const created = `{"token_id":"T1","token":"ak_pat_T1_secret","name":"svc","permissions":["workspaces:read","patches:write"],"workspace":"s"}`
	h := newHub(t, http.StatusCreated, created)
	out, errOut, code := runTokens(t, BuildRootCommand(), h,
		"tokens", "create", "--workspace", "s", "--name", "svc", "--permissions", " workspaces:read, ,patches:write", "--expires", "30")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	req := h.only(t)
	want := map[string]any{"name": "svc", "permissions": []any{"workspaces:read", "patches:write"}, "expires": 30.0}
	if req.Method != http.MethodPost || req.Path != "/api/v1/workspaces/s/tokens" || !reflect.DeepEqual(decodeJSON(t, req.Body), any(want)) {
		t.Errorf("request = %+v; want POST /api/v1/workspaces/s/tokens with %v", req, want)
	}
	if got := decodeOneDocument(t, out); !reflect.DeepEqual(got, decodeJSON(t, created)) {
		t.Errorf("stdout = %q; want the hub's answer once", out)
	}
	if lines := strings.Split(strings.TrimSpace(errOut), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "will not be shown again") {
		t.Errorf("stderr = %q; want one warning line", errOut)
	}
}

// list --workspace prints the workspace's bound tokens.
func TestTokensList_WorkspaceListsBoundTokens(t *testing.T) {
	const list = `[{"token_id":"T1","name":"a","workspace":"s"},{"token_id":"T2","name":"b","workspace":"s","revoked_at":"y"}]`
	h := newHub(t, http.StatusOK, list)
	out, _, code := runTokens(t, BuildRootCommand(), h, "tokens", "list", "--workspace", "s")
	if req := h.only(t); code != 0 || req.Method != http.MethodGet || req.Path != "/api/v1/workspaces/s/tokens" {
		t.Fatalf("exit %d, request %+v; want 0 and GET /api/v1/workspaces/s/tokens", code, req)
	}
	if got := decodeOneDocument(t, out); !reflect.DeepEqual(got, decodeJSON(t, list)) {
		t.Errorf("stdout = %q; want the hub's list", out)
	}
}

// An empty --workspace, a missing or empty --name and no permission exit 2
// before any request.
func TestTokens_UsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"create", "--workspace", "", "--name", "n", "--permissions", "p"},
		{"create", "--workspace", "s", "--permissions", "p"},
		{"create", "--workspace", "s", "--name", " ", "--permissions", "p"},
		{"create", "--workspace", "s", "--name", "n"},
		{"create", "--workspace", "s", "--name", "n", "--permissions", " , ,"},
		{"list", "--workspace", " "},
	} {
		h := newHub(t, http.StatusOK, `{}`)
		stdout, _, code := runTokens(t, BuildRootCommand(), h, append([]string{"tokens"}, args...)...)
		if code != 2 {
			t.Errorf("%v: exit %d, stdout %q; want 2", args, code, stdout)
		}
		h.none(t)
	}
}

// A hub refusal of a bound create or list exits 1 with the hub's message.
func TestTokens_HubErrorsAreReported(t *testing.T) {
	for _, args := range [][]string{
		{"tokens", "create", "--workspace", "s", "--name", "n", "--permissions", "p"},
		{"tokens", "list", "--workspace", "s"},
	} {
		h := newHub(t, http.StatusForbidden, errorBody(403, "this token is bound to workspace S"))
		out, _, code := runTokens(t, BuildRootCommand(), h, args...)
		if code != 1 || errorMessage(t, out) != "this token is bound to workspace S" {
			t.Errorf("%v: exit %d, stdout %q; want 1 and the hub's message", args, code, out)
		}
	}
}

// Wrapping a tokens command that lacks create or list is refused, so a
// change in apikit's tree is caught rather than silently losing --workspace.
func TestWrapTokensCmd_NeedsCreateAndList(t *testing.T) {
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
