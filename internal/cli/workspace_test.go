package cli

import (
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const workspaceAnswer = `{"slug":"ws","git_url":"https://github.com/o/r","status":"ready"}`

// create turns each flag into the matching field of POST /workspaces.
func TestWorkspaceCreate_SendsFlagsAsBodyFields(t *testing.T) {
	const gitURL = "https://github.com/o/r"
	for _, tc := range []struct {
		name string
		args []string
		body map[string]any
	}{
		{"the required flags only", nil, map[string]any{}},
		{"metadata and org",
			[]string{"--branch", "dev", "--org", "acme", "--display-name", "Widgets", "--description", ""},
			map[string]any{"branch": "dev", "org_id": "acme", "display_name": "Widgets", "description": ""}},
		{"sync disabled", []string{"--setting", "sync_enabled=false"}, map[string]any{"settings": map[string]any{"sync_enabled": false}}},
		{"carry_patch mode",
			[]string{"--workspace-mode", "carry_patch", "--upstream-url", "https://github.com/up/r", "--integration-branch", "deploy"},
			map[string]any{"workspace_mode": "carry_patch", "upstream_url": "https://github.com/up/r", "integration_branch": "deploy"}},
		{"settings typed",
			[]string{"--setting", "a=true", "--setting", "b=false", "--setting", "c=", "--setting", "d=x=y"},
			map[string]any{"settings": map[string]any{"a": true, "b": false, "c": nil, "d": "x=y"}}},
		{"a PAT", []string{"--git-pat", "ghp_x"}, map[string]any{"git_pat": "ghp_x"}},
		{"a username and password",
			[]string{"--git-username", "bob", "--git-password", "pw"},
			map[string]any{"git_username": "bob", "git_password": "pw"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHub(t, http.StatusCreated, workspaceAnswer)
			stdout, _, err := h.run(t, append([]string{"workspace", "create", "--git-url", gitURL, "--slug", "ws"}, tc.args...)...)
			if err != nil {
				t.Fatalf("workspace create: %v", err)
			}
			req := h.only(t)
			want := map[string]any{"slug": "ws", "git_url": gitURL}
			for k, v := range tc.body {
				want[k] = v
			}
			if req.Method != http.MethodPost || req.Path != "/api/v1/workspaces" {
				t.Errorf("request = %s %s; want POST /api/v1/workspaces", req.Method, req.Path)
			}
			if got := decodeJSON(t, req.Body); !reflect.DeepEqual(got, any(want)) {
				t.Errorf("body = %v; want %v", got, want)
			}
			if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, decodeJSON(t, workspaceAnswer)) {
				t.Errorf("stdout = %q; want the hub's answer", stdout)
			}
		})
	}
}

// create checks its flags before it sends anything: exit 2 and no request.
func TestWorkspaceCreate_UsageErrors(t *testing.T) {
	base := []string{"workspace", "create", "--git-url", "https://github.com/o/r", "--slug", "ws"}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no git url", []string{"workspace", "create", "--slug", "ws"}, "--git-url flag is required"},
		{"no slug", []string{"workspace", "create", "--git-url", "https://x/r"}, "--slug flag is required"},
		{"pat with username", append(base, "--git-pat", "p", "--git-username", "u", "--git-password", "w"), "mutually exclusive"},
		{"username alone", append(base, "--git-username", "u"), "must be provided together"},
		{"password alone", append(base, "--git-password", "w"), "must be provided together"},
		{"empty pat", append(base, "--git-pat", ""), "--git-pat value must not be empty"},
		{"empty username", append(base, "--git-username", "", "--git-password", "w"), "--git-username value must not be empty"},
		{"empty password", append(base, "--git-username", "u", "--git-password", ""), "values must not be empty"},
		{"credentials over ssh", []string{"workspace", "create", "--git-url", "git@github.com:o/r.git", "--slug", "ws", "--git-pat", "p"}, "require an HTTPS git_url"},
		{"credentials over http", []string{"workspace", "create", "--git-url", "http://x/r", "--slug", "ws", "--git-pat", "p"}, "require an HTTPS git_url"},
		{"bad workspace mode", append(base, "--workspace-mode", "fork"), "invalid --workspace-mode"},
		{"carry_patch without upstream", append(base, "--workspace-mode", "carry_patch"), "--upstream-url is required"},
		{"upstream in standard mode", append(base, "--upstream-url", "https://x/u"), "--upstream-url is not allowed"},
		{"integration branch in standard mode", append(base, "--integration-branch", "deploy"), "--integration-branch is not allowed"},
		{"a setting without =", append(base, "--setting", "novalue"), "--setting takes key=value"},
		{"a setting with an empty key", append(base, "--setting", " =v"), "--setting takes key=value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHub(t, http.StatusCreated, workspaceAnswer)
			stdout, _, err := h.run(t, tc.args...)
			assertUsageError(t, h, stdout, err, tc.want)
		})
	}
}

// create --wait prints the create response, polls the workspace until it is
// ready and prints the final record; a failed clone exits 1.
func TestWorkspaceCreate_WaitPollsTheStatus(t *testing.T) {
	for _, tc := range []struct {
		final    string
		exitCode int
	}{{"ready", 0}, {"failed", 1}} {
		t.Run(tc.final, func(t *testing.T) {
			var polls atomic.Int32
			h := newHubFunc(t, func(w http.ResponseWriter, r request) {
				status := "cloning"
				if r.Method == http.MethodGet && polls.Add(1) >= 2 {
					status = tc.final
				}
				respond(w, http.StatusOK, `{"slug":"ws","status":"`+status+`"}`)
			})
			stdout, stderr, err := h.run(t, "workspace", "create", "--git-url", "https://x/r", "--slug", "ws", "--wait", "--poll-interval", "20ms")
			if code := ExitCode(err); code != tc.exitCode {
				t.Errorf("exit code = %d (%v); want %d", code, err, tc.exitCode)
			}
			docs := decodeDocuments(t, stdout)
			if len(docs) < 2 || docs[0].(map[string]any)["status"] != "cloning" || docs[1].(map[string]any)["status"] != tc.final {
				t.Errorf("stdout = %q; want the create response then the %s record", stdout, tc.final)
			}
			if reqs := h.requests(); len(reqs) != 3 || reqs[2].Path != "/api/v1/workspaces/ws" {
				t.Errorf("requests = %+v; want the create and two polls of /api/v1/workspaces/ws", reqs)
			}
			if tc.final == "failed" && (!strings.Contains(stderr, "Workspace clone failed.") || errorMessage(t, stdout) != "workspace clone failed") {
				t.Errorf("stdout = %q, stderr = %q; want the failure reported", stdout, stderr)
			}
		})
	}
}

// Each read and write sends its route, with the slug and branch escaped as
// path segments and the flags as query parameters, and prints the answer.
func TestWorkspace_CommandsSendTheirRoutes(t *testing.T) {
	const slug = "a/b c"
	base := "/api/v1" + apiPath("workspaces", slug)
	for _, tc := range []struct {
		args         []string
		method, path string
		query, body  string
	}{
		{[]string{"list"}, http.MethodGet, "/api/v1/workspaces", "", ""},
		{[]string{"list", "--include-archived"}, http.MethodGet, "/api/v1/workspaces", "include_archived=true", ""},
		{[]string{"get", slug}, http.MethodGet, base, "", ""},
		{[]string{"archive", slug}, http.MethodPost, base + "/archive", "", ""},
		{[]string{"reactivate", slug}, http.MethodPost, base + "/reactivate", "", ""},
		{[]string{"sync", slug}, http.MethodPost, base + "/sync", "", ""},
		{[]string{"sync", slug, "--reset-to-upstream"}, http.MethodPost, base + "/sync", "reset_to_upstream=true", ""},
		{[]string{"reclone", slug, "--confirm"}, http.MethodPost, base + "/reclone", "", ""},
		{[]string{"push", slug, "fix/1 a"}, http.MethodPost, base + "/branches/fix%2F1%20a/push", "", `{"force":false}`},
		{[]string{"update", slug, "--display-name", "x"}, http.MethodPatch, base, "", `{"display_name":"x"}`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			h := newHub(t, http.StatusOK, workspaceAnswer)
			stdout, stderr, err := h.run(t, append([]string{"workspace"}, tc.args...)...)
			if err != nil {
				t.Fatalf("workspace %v: %v", tc.args, err)
			}
			req := h.only(t)
			if req.Method != tc.method || req.Path != tc.path || req.Query != tc.query || req.Auth != "Bearer "+testAPIKey {
				t.Errorf("request = %+v; want %s %s?%s with the API key", req, tc.method, tc.path, tc.query)
			}
			if tc.body == "" && req.Body != "" || tc.body != "" && !reflect.DeepEqual(decodeJSON(t, req.Body), decodeJSON(t, tc.body)) {
				t.Errorf("body = %q; want %q", req.Body, tc.body)
			}
			if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, decodeJSON(t, workspaceAnswer)) || stderr != "" {
				t.Errorf("stdout = %q, stderr = %q; want the hub's answer and nothing else", stdout, stderr)
			}
		})
	}
}

// delete and reclone refuse to run without --confirm; delete prints a
// status document of its own, since the hub answers 204.
func TestWorkspace_DeleteAndRecloneNeedConfirm(t *testing.T) {
	for _, cmd := range []string{"delete", "reclone"} {
		h := newHub(t, http.StatusNoContent, "")
		stdout, _, err := h.run(t, "workspace", cmd, "ws")
		assertUsageError(t, h, stdout, err, "--confirm flag is required")
	}

	h := newHub(t, http.StatusNoContent, "")
	stdout, stderr, err := h.run(t, "workspace", "delete", "ws", "--confirm")
	if err != nil {
		t.Fatalf("workspace delete: %v", err)
	}
	if req := h.only(t); req.Method != http.MethodDelete || req.Path != "/api/v1/workspaces/ws" {
		t.Errorf("request = %+v; want DELETE /api/v1/workspaces/ws", req)
	}
	if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, any(map[string]any{"status": "deleted", "slug": "ws"})) {
		t.Errorf("stdout = %q; want the deleted status document", stdout)
	}
	if !strings.Contains(stderr, "Workspace 'ws' has been permanently deleted.") {
		t.Errorf("stderr = %q; want the confirmation", stderr)
	}
}

// update sends only the fields that were given, null for the --clear flags,
// and refuses to run with no change.
func TestWorkspaceUpdate_SendsOnlyTheGivenFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		body map[string]any
	}{
		{"a display name", []string{"--display-name", "Widgets"}, map[string]any{"display_name": "Widgets"}},
		{"a description and an org", []string{"--description", "d", "--org", "acme"}, map[string]any{"description": "d", "org_id": "acme"}},
		{"clears", []string{"--clear-display-name", "--clear-description", "--clear-org"},
			map[string]any{"display_name": nil, "description": nil, "org_id": nil}},
		{"a clear over a value", []string{"--display-name", "x", "--clear-display-name"}, map[string]any{"display_name": nil}},
		{"settings", []string{"--setting", "patch_branch_source=origin", "--setting", "auto_sync="},
			map[string]any{"settings": map[string]any{"patch_branch_source": "origin", "auto_sync": nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHub(t, http.StatusOK, workspaceAnswer)
			if _, _, err := h.run(t, append([]string{"workspace", "update", "ws"}, tc.args...)...); err != nil {
				t.Fatalf("workspace update: %v", err)
			}
			if got := decodeJSON(t, h.only(t).Body); !reflect.DeepEqual(got, any(tc.body)) {
				t.Errorf("body = %v; want %v", got, tc.body)
			}
		})
	}

	h := newHub(t, http.StatusOK, workspaceAnswer)
	stdout, _, err := h.run(t, "workspace", "update", "ws")
	assertUsageError(t, h, stdout, err, "at least one update flag")
	stdout, _, err = h.run(t, "workspace", "update", "ws", "--setting", "bad")
	assertUsageError(t, h, stdout, err, "--setting takes key=value")
}

// update refuses an answer that is not a workspace: no JSON, or no slug.
func TestWorkspaceUpdate_RejectsAMalformedAnswer(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"not-json", "invalid"},
		{`{"status":"ready"}`, "missing required field 'slug'"},
	} {
		h := newHub(t, http.StatusOK, tc.body)
		stdout, _, err := h.run(t, "workspace", "update", "ws", "--display-name", "x")
		if err == nil || !strings.Contains(errorMessage(t, stdout), tc.want) {
			t.Errorf("answer %q: err = %v, stdout = %q; want %q reported", tc.body, err, stdout, tc.want)
		}
	}
}

// syncHub answers a sync with the diverged branches and, when rebuild is
// set, a rebuild job that ends with that status.
func syncHub(t *testing.T, diverged []string, rebuild string) *hub {
	t.Helper()
	return newHubFunc(t, func(w http.ResponseWriter, r request) {
		if r.Method == http.MethodGet {
			respond(w, http.StatusOK, `{"id":"rb-1","status":"`+rebuild+`"}`)
			return
		}
		body := map[string]any{"patches_merged": []string{}, "patches_diverged": diverged, "rebuild_triggered": rebuild != ""}
		if rebuild != "" {
			body["rebuild_job_id"] = "rb-1"
		}
		respond(w, http.StatusOK, marshalJSON(t, body))
	})
}

// sync --fail-on-diverged exits 3 when the response lists diverged patch
// branches, with the sync response as the only document on stdout and the
// branches named once on stderr; otherwise, and without the flag, it exits 0.
func TestWorkspaceSync_FailOnDiverged(t *testing.T) {
	for _, tc := range []struct {
		name     string
		diverged []string
		flag     bool
		exitCode int
	}{
		{"diverged with the flag", []string{"feat-a", "feat-b"}, true, 3},
		{"none diverged with the flag", []string{}, true, 0},
		{"diverged without the flag", []string{"feat-a"}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := syncHub(t, tc.diverged, "")
			args := []string{"workspace", "sync", "ws"}
			if tc.flag {
				args = append(args, "--fail-on-diverged")
			}
			stdout, stderr, err := h.run(t, args...)
			if code := ExitCode(err); code != tc.exitCode || IsDivergedError(err) != (tc.exitCode == 3) {
				t.Errorf("exit code = %d (%v); want %d", code, err, tc.exitCode)
			}
			doc, _ := decodeOneDocument(t, stdout).(map[string]any)
			if _, hasError := doc["error"]; hasError || doc["patches_diverged"] == nil {
				t.Errorf("stdout = %q; want the sync response alone", stdout)
			}
			if tc.exitCode != 3 {
				if stderr != "" {
					t.Errorf("stderr = %q; want nothing", stderr)
				}
				return
			}
			if want := "diverged patch branches: feat-a, feat-b\n"; stderr != want {
				t.Errorf("stderr = %q; want %q", stderr, want)
			}
		})
	}
}

// sync --wait polls a triggered rebuild to its end and prints both records;
// without a rebuild it sends nothing more. The diverged check runs after the
// wait, and a failed rebuild (exit 1) takes precedence over diverged (3).
func TestWorkspaceSync_WaitThenDivergedCheck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rebuild  string
		requests int
		exitCode int
	}{
		{"rebuild completed", "completed", 2, 3},
		{"rebuild failed", "failed", 2, 1},
		{"no rebuild", "", 1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := syncHub(t, []string{"a"}, tc.rebuild)
			stdout, _, err := h.run(t, "workspace", "sync", "ws", "--wait", "--fail-on-diverged", "--poll-interval", "20ms")
			if code := ExitCode(err); code != tc.exitCode {
				t.Errorf("exit code = %d (%v); want %d", code, err, tc.exitCode)
			}
			reqs := h.requests()
			if len(reqs) != tc.requests || tc.requests == 2 && reqs[1].Path != "/api/v1/workspaces/ws/rebuilds/rb-1" {
				t.Errorf("requests = %+v; want %d, the poll on /api/v1/workspaces/ws/rebuilds/rb-1", reqs, tc.requests)
			}
			if docs := decodeDocuments(t, stdout); len(docs) < tc.requests || tc.rebuild != "" && docs[1].(map[string]any)["status"] != tc.rebuild {
				t.Errorf("stdout = %q; want the sync response and the rebuild record", stdout)
			}
		})
	}

	// Without --fail-on-diverged a completed rebuild wait exits 0.
	h := syncHub(t, []string{"a"}, "completed")
	if _, _, err := h.run(t, "workspace", "sync", "ws", "--wait", "--poll-interval", "20ms"); err != nil {
		t.Errorf("sync --wait: %v; want success", err)
	}
}

// push --force says on stderr, before the request, that origin's branch may
// be overwritten, and sends force true.
func TestWorkspacePush_ForceWarnsAndIsSent(t *testing.T) {
	h := newHub(t, http.StatusOK, `{"result":"forced"}`)
	stdout, stderr, err := h.run(t, "workspace", "push", "ws", "feat", "--force")
	if err != nil {
		t.Fatalf("workspace push --force: %v", err)
	}
	if req := h.only(t); req.Body != `{"force":true}` {
		t.Errorf("body = %q; want force true", req.Body)
	}
	if want := "warning: --force: origin's feat may be overwritten with the workspace's branch\n"; stderr != want {
		t.Errorf("stderr = %q; want %q", stderr, want)
	}
	if doc, _ := decodeOneDocument(t, stdout).(map[string]any); doc["result"] != "forced" {
		t.Errorf("stdout = %q; want the hub's answer", stdout)
	}
}

// A hub error on any command exits 1 with the hub's envelope as the only
// document on stdout; an unreachable hub is a client error, exit 2.
func TestWorkspace_HubErrorsAreReported(t *testing.T) {
	for _, args := range [][]string{
		{"create", "--git-url", "https://x/r", "--slug", "ws"}, {"list"}, {"get", "ws"}, {"update", "ws", "--org", "o"},
		{"archive", "ws"}, {"reactivate", "ws"}, {"delete", "ws", "--confirm"}, {"sync", "ws", "--wait"},
		{"reclone", "ws", "--confirm"}, {"push", "ws", "feat"},
	} {
		t.Run(args[0], func(t *testing.T) {
			h := newHub(t, http.StatusNotFound, errorBody(404, "workspace not found"))
			stdout, _, err := h.run(t, append([]string{"workspace"}, args...)...)
			assertEnvelopeOnly(t, stdout, err, "workspace not found")
			h.only(t)
		})
	}

	h := newHub(t, http.StatusOK, workspaceAnswer)
	h.Close()
	stdout, _, err := h.run(t, "workspace", "get", "ws")
	if code := ExitCode(err); code != 2 || !strings.Contains(stdout, `"error"`) {
		t.Errorf("unreachable hub: exit %d, stdout %q; want 2 and the client error reported", code, stdout)
	}
}

// A wrong number of positional arguments is a usage error and sends nothing.
func TestWorkspace_ArgumentCountsAreChecked(t *testing.T) {
	for _, args := range [][]string{
		{"get"}, {"get", "a", "b"}, {"archive"}, {"reactivate"}, {"delete"}, {"update"},
		{"sync"}, {"reclone"}, {"push"}, {"push", "ws"}, {"push", "ws", "a", "b"},
	} {
		h := newHub(t, http.StatusOK, workspaceAnswer)
		stdout, _, err := h.run(t, append([]string{"workspace"}, args...)...)
		assertUsageError(t, h, stdout, err, "")
	}
}
