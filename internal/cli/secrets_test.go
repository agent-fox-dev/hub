package cli

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// secrets and vars share one command shape: KEY=VALUE arguments, the
// --user/--org/--workspace scope flags and the value from stdin. The tables
// below run both.

var keyValueResources = []string{"secrets", "vars"}

// entries returns the KEY=VALUE pairs of a create body.
func entries(t *testing.T, body string) []string {
	t.Helper()
	doc, _ := decodeJSON(t, body).(map[string]any)
	list, _ := doc["entries"].([]any)
	var out []string
	for _, e := range list {
		m, _ := e.(map[string]any)
		out = append(out, m["key"].(string)+"="+m["value"].(string))
	}
	return out
}

// Each subcommand sends its route under the scope the flags name, user by
// default, with the key escaped as a path segment.
func TestKeyValueCommands_SendTheirRoutes(t *testing.T) {
	for _, res := range keyValueResources {
		for _, tc := range []struct {
			args         []string
			method, path string
			body         string
		}{
			{[]string{"create", "A=1"}, http.MethodPost, "/api/v1/user/" + res, `{"entries":[{"key":"A","value":"1"}]}`},
			{[]string{"create", "--org", "my org", "A=1"}, http.MethodPost, "/api/v1/orgs/my%20org/" + res, `{"entries":[{"key":"A","value":"1"}]}`},
			{[]string{"create", "--workspace", "w", "A=1"}, http.MethodPost, "/api/v1/workspaces/w/" + res, `{"entries":[{"key":"A","value":"1"}]}`},
			{[]string{"list"}, http.MethodGet, "/api/v1/user/" + res, ""},
			{[]string{"list", "--user"}, http.MethodGet, "/api/v1/user/" + res, ""},
			{[]string{"list", "--org", "o"}, http.MethodGet, "/api/v1/orgs/o/" + res, ""},
			{[]string{"list", "--workspace", "w"}, http.MethodGet, "/api/v1/workspaces/w/" + res, ""},
			{[]string{"update", "K=v=w"}, http.MethodPatch, "/api/v1/user/" + res + "/K", `{"value":"v=w"}`},
			{[]string{"update", "--org", "o", "a/b c=v"}, http.MethodPatch, "/api/v1/orgs/o/" + res + "/a%2Fb%20c", `{"value":"v"}`},
			{[]string{"update", "--workspace", "w", "K=v"}, http.MethodPatch, "/api/v1/workspaces/w/" + res + "/K", `{"value":"v"}`},
			{[]string{"delete", "a/b c"}, http.MethodDelete, "/api/v1/user/" + res + "/a%2Fb%20c", ""},
			{[]string{"delete", "--org", "o", "K"}, http.MethodDelete, "/api/v1/orgs/o/" + res + "/K", ""},
			{[]string{"delete", "--workspace", "w", "K"}, http.MethodDelete, "/api/v1/workspaces/w/" + res + "/K", ""},
		} {
			t.Run(res+" "+strings.Join(tc.args, " "), func(t *testing.T) {
				h := newHub(t, http.StatusOK, `{"key":"K"}`)
				stdout, stderr, err := h.run(t, append([]string{res}, tc.args...)...)
				if err != nil {
					t.Fatalf("%s %v: %v", res, tc.args, err)
				}
				req := h.only(t)
				if req.Method != tc.method || req.Path != tc.path {
					t.Errorf("request = %s %s; want %s %s", req.Method, req.Path, tc.method, tc.path)
				}
				if tc.body == "" && req.Body != "" || tc.body != "" && !reflect.DeepEqual(decodeJSON(t, req.Body), decodeJSON(t, tc.body)) {
					t.Errorf("body = %q; want %q", req.Body, tc.body)
				}
				if tc.args[0] == "delete" {
					if stdout != "" || !strings.Contains(stderr, "has been deleted") {
						t.Errorf("stdout = %q, stderr = %q; want nothing and a confirmation", stdout, stderr)
					}
					return
				}
				if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, any(map[string]any{"key": "K"})) {
					t.Errorf("stdout = %q; want the hub's answer", stdout)
				}
			})
		}
	}
}

// create parses every argument as a comma-separated list of KEY=VALUE, the
// value split at the first '=', or reads the one KEY's value from stdin with
// one trailing line break removed.
func TestKeyValueCreate_ParsesEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
		want  []string
	}{
		{"several arguments and commas", []string{"A=1", "B=2,C=3"}, "", []string{"A=1", "B=2", "C=3"}},
		{"a value with =", []string{"URL=a=b"}, "", []string{"URL=a=b"}},
		{"stdin", []string{"TOKEN", "--from-stdin"}, "s3cr3t\n", []string{"TOKEN=s3cr3t"}},
		{"stdin with CRLF", []string{"TOKEN", "--from-stdin"}, "s3cr3t\r\n", []string{"TOKEN=s3cr3t"}},
		{"stdin keeps inner whitespace", []string{"TOKEN", "--from-stdin"}, " a b \n\n", []string{"TOKEN= a b \n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHub(t, http.StatusCreated, `{}`)
			if _, _, err := h.runStdin(t, tc.stdin, append([]string{"secrets", "create"}, tc.args...)...); err != nil {
				t.Fatalf("secrets create: %v", err)
			}
			if got := entries(t, h.only(t).Body); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("entries = %q; want %q", got, tc.want)
			}
		})
	}
}

// update --from-stdin reads the new value from stdin.
func TestKeyValueUpdate_ReadsTheValueFromStdin(t *testing.T) {
	h := newHub(t, http.StatusOK, `{}`)
	if _, _, err := h.runStdin(t, "new\n", "vars", "update", "TOKEN", "--from-stdin"); err != nil {
		t.Fatalf("vars update: %v", err)
	}
	if req := h.only(t); req.Path != "/api/v1/user/vars/TOKEN" || req.Body != `{"value":"new"}` {
		t.Errorf("request = %+v; want PATCH /api/v1/user/vars/TOKEN with value new", req)
	}
}

// create with several scope flags writes to each, user then org then
// workspace, and prints every answer; a failed scope is reported and the
// others still written, with exit 1.
func TestKeyValueCreate_WritesEveryScopeInOrder(t *testing.T) {
	want := []string{"/api/v1/user/vars", "/api/v1/orgs/o/vars", "/api/v1/workspaces/w/vars"}
	h := newHub(t, http.StatusCreated, `{"created":1}`)
	stdout, _, err := h.run(t, "vars", "create", "--workspace", "w", "--user", "--org", "o", "A=1")
	if err != nil {
		t.Fatalf("vars create: %v", err)
	}
	var paths []string
	for _, r := range h.requests() {
		paths = append(paths, r.Path)
	}
	if !reflect.DeepEqual(paths, want) || len(decodeDocuments(t, stdout)) != 3 {
		t.Errorf("paths = %v, stdout = %q; want %v and three documents", paths, stdout, want)
	}

	h = newHubFunc(t, func(w http.ResponseWriter, r request) {
		if strings.HasPrefix(r.Path, "/api/v1/orgs/") {
			respond(w, http.StatusForbidden, errorBody(403, "not a member"))
			return
		}
		respond(w, http.StatusCreated, `{"created":1}`)
	})
	stdout, _, err = h.run(t, "secrets", "create", "--user", "--org", "o", "--workspace", "w", "A=1")
	if code := ExitCode(err); code != 1 {
		t.Errorf("exit code = %d (%v); want 1 after a failed scope", code, err)
	}
	if n := len(h.requests()); n != 3 {
		t.Errorf("the hub received %d requests; want all three scopes tried", n)
	}
	if docs := decodeDocuments(t, stdout); len(docs) != 3 || errorMessage(t, stdout) != "not a member" {
		t.Errorf("stdout = %q; want two answers and the org's error envelope", stdout)
	}
}

// vars resolve reads a workspace's resolved variables; it takes no scope flag.
func TestVarsResolve_SendsTheRoute(t *testing.T) {
	h := newHub(t, http.StatusOK, `{"A":"1"}`)
	stdout, _, err := h.run(t, "vars", "resolve", "a/b")
	if err != nil {
		t.Fatalf("vars resolve: %v", err)
	}
	if req := h.only(t); req.Method != http.MethodGet || req.Path != "/api/v1/workspaces/a%2Fb/vars/resolved" {
		t.Errorf("request = %+v; want GET /api/v1/workspaces/a%%2Fb/vars/resolved", req)
	}
	if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, any(map[string]any{"A": "1"})) {
		t.Errorf("stdout = %q; want the hub's answer", stdout)
	}
}

// Mistakes in the arguments and the scope flags exit 2 before anything is sent.
func TestKeyValueCommands_UsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"secrets", "create"}, "required argument: KEY=VALUE"},
		{[]string{"vars", "create", "A"}, "missing '='"},
		{[]string{"secrets", "create", "A=1,,B=2"}, "empty key in argument at position 1"},
		{[]string{"vars", "create", "A=1, =2"}, "empty key in argument at position 1"},
		{[]string{"secrets", "create", "A=1", "--from-stdin"}, "exactly one KEY"},
		{[]string{"vars", "create", "A,B", "--from-stdin"}, "exactly one KEY"},
		{[]string{"secrets", "create", "A", "B", "--from-stdin"}, "exactly one KEY"},
		{[]string{"secrets", "create", " ", "--from-stdin"}, "empty key"},
		{[]string{"secrets", "list", "--user", "--org", "o"}, "only one of --user, --org, --workspace"},
		{[]string{"vars", "list", "--org", "o", "--workspace", "w"}, "only one of --user, --org, --workspace"},
		{[]string{"secrets", "update"}, "required argument: KEY=VALUE"},
		{[]string{"vars", "update", "A"}, "missing '='"},
		{[]string{"secrets", "update", "A=1", "--from-stdin"}, "bare KEY"},
		{[]string{"vars", "update", " ", "--from-stdin"}, "empty key"},
		{[]string{"secrets", "update", "K=v", "--user", "--workspace", "w"}, "only one of"},
		{[]string{"vars", "delete"}, "required argument: KEY"},
		{[]string{"secrets", "delete", "K", "--org", "o", "--workspace", "w"}, "only one of"},
		{[]string{"vars", "resolve"}, "required argument: workspace slug"},
		{[]string{"vars", "resolve", "w", "--org", "o"}, "unknown flag"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			h := newHub(t, http.StatusOK, `{}`)
			stdout, _, err := h.runStdin(t, "x", tc.args...)
			assertUsageError(t, h, stdout, err, tc.want)
		})
	}
}

// A hub error exits 1 with the hub's envelope as the only document on stdout.
func TestKeyValueCommands_HubErrorsAreReported(t *testing.T) {
	for _, res := range keyValueResources {
		for _, args := range [][]string{{"create", "A=1"}, {"list"}, {"update", "K=v"}, {"delete", "K"}} {
			h := newHub(t, http.StatusNotFound, errorBody(404, res+" not found"))
			stdout, _, err := h.run(t, append([]string{res}, args...)...)
			assertEnvelopeOnly(t, stdout, err, res+" not found")
		}
	}
	h := newHub(t, http.StatusNotFound, errorBody(404, "workspace not found"))
	stdout, _, err := h.run(t, "vars", "resolve", "w")
	assertEnvelopeOnly(t, stdout, err, "workspace not found")
}
