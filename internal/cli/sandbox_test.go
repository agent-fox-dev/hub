package cli

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	leaseToken = "sbl_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	leaseID    = "sbx_0123456789abcdef0123456789abcdef"
	leaseBody  = `{"id":"` + leaseID + `","workspace":"widgets","profile":"tools","state":"leased","labels":{}}`
	profiles   = `{"profiles":[{"name":"agent","default":true,"image":"registry.example/agent:1"}]}`
)

// leaseHub answers a create with a lease that carries the token, a destroy
// with 204 and every read with the lease.
func leaseHub(t *testing.T) *hub {
	t.Helper()
	return newHubFunc(t, func(w http.ResponseWriter, r request) {
		switch r.Method {
		case http.MethodPost:
			respond(w, http.StatusCreated, strings.TrimSuffix(leaseBody, "}")+`,"token":"`+leaseToken+`"}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			respond(w, http.StatusOK, leaseBody)
		}
	})
}

// The reads and destroy send their routes and print the answer; destroy
// prints a status document of its own, since the hub answers 204.
func TestSandbox_CommandsSendTheirRoutes(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
		query        string
		stdout       string
	}{
		{[]string{"profiles"}, http.MethodGet, "/api/v1/sandbox-profiles", "", profiles},
		{[]string{"list", "widgets"}, http.MethodGet, "/api/v1/workspaces/widgets/sandboxes", "", leaseBody},
		{[]string{"list", "widgets", "--all"}, http.MethodGet, "/api/v1/workspaces/widgets/sandboxes", "all=true", leaseBody},
		{[]string{"get", "widgets", leaseID}, http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/" + leaseID, "", leaseBody},
		{[]string{"destroy", "widgets", "sbx x"}, http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/sbx%20x", "", `{"status":"destroyed","id":"sbx x"}`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			h := newHubFunc(t, func(w http.ResponseWriter, r request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				respond(w, http.StatusOK, tc.stdout)
			})
			stdout, stderr, err := h.run(t, append([]string{"sandbox"}, tc.args...)...)
			if err != nil {
				t.Fatalf("sandbox %v: %v", tc.args, err)
			}
			req := h.only(t)
			if req.Method != tc.method || req.Path != tc.path || req.Query != tc.query || req.Auth != "Bearer "+testAPIKey {
				t.Errorf("request = %+v; want %s %s?%s with the API key", req, tc.method, tc.path, tc.query)
			}
			if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, decodeJSON(t, tc.stdout)) {
				t.Errorf("stdout = %q; want %s", stdout, tc.stdout)
			}
			if tc.args[0] == "destroy" && !strings.Contains(stderr, "Sandbox lease sbx x destroyed.") {
				t.Errorf("stderr = %q; want the confirmation", stderr)
			}
		})
	}
}

// create sends the flags that are set, --ttl unparsed and the labels as a
// map, prints the lease with its token and warns on stderr that the token
// is shown once.
func TestSandboxCreate_PrintsTheLeaseAndWarns(t *testing.T) {
	h := leaseHub(t)
	stdout, stderr, err := h.run(t, "sandbox", "create", "widgets", "--purpose", "triage", "--ttl", "soon", "--label", "k=v", "--label", "a=b=c")
	if err != nil {
		t.Fatalf("sandbox create: %v", err)
	}
	req := h.only(t)
	want := map[string]any{"purpose": "triage", "ttl": "soon", "labels": map[string]any{"k": "v", "a": "b=c"}}
	if req.Method != http.MethodPost || req.Path != "/api/v1/workspaces/widgets/sandboxes" || !reflect.DeepEqual(decodeJSON(t, req.Body), any(want)) {
		t.Errorf("request = %+v; want POST .../widgets/sandboxes with %v", req, want)
	}
	if doc, _ := decodeOneDocument(t, stdout).(map[string]any); doc["token"] != leaseToken || doc["id"] != leaseID {
		t.Errorf("stdout = %q; want the lease with its token", stdout)
	}
	if !strings.Contains(stderr, "will not be shown again") || strings.Contains(stderr, leaseToken) {
		t.Errorf("stderr = %q; want the warning without the token", stderr)
	}
}

// create --token-file writes the token alone to a 0600 file, truncating and
// narrowing one that exists, and prints the lease without the token.
func TestSandboxCreate_TokenFileKeepsTheTokenOffStdout(t *testing.T) {
	h := leaseHub(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("old content that is much longer than the token will ever be, for sure"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := h.run(t, "sandbox", "create", "widgets", "--profile", "tools", "--token-file", path)
	if err != nil {
		t.Fatalf("sandbox create: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != leaseToken || info.Mode().Perm() != 0o600 {
		t.Errorf("token file holds %q with mode %v; want the token alone with mode 0600", got, info.Mode().Perm())
	}
	doc, _ := decodeOneDocument(t, stdout).(map[string]any)
	if _, has := doc["token"]; has || doc["id"] != leaseID || strings.Contains(stdout+stderr, leaseToken) {
		t.Errorf("stdout = %q, stderr = %q; want the lease without the token", stdout, stderr)
	}
	if body, _ := decodeJSON(t, h.only(t).Body).(map[string]any); !reflect.DeepEqual(body, map[string]any{"profile": "tools"}) {
		t.Errorf("body = %v; want the profile alone", body)
	}
}

// When the token cannot be written after the lease was issued, create
// releases the lease, says whether that worked and exits 1; the token never
// reaches stdout.
func TestSandboxCreate_FailedTokenWriteReleasesTheLease(t *testing.T) {
	prev := writeTokenFile
	writeTokenFile = func(*os.File, string) error { return errors.New("disk full") }
	t.Cleanup(func() { writeTokenFile = prev })

	for _, tc := range []struct {
		name         string
		deleteStatus int
		want, reject string
	}{
		{"release succeeds", http.StatusNoContent, "was released", "could not be released"},
		{"release fails", http.StatusInternalServerError, "could not be released; end it with: afc sandbox destroy widgets " + leaseID, "was released"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHubFunc(t, func(w http.ResponseWriter, r request) {
				if r.Method == http.MethodDelete {
					respond(w, tc.deleteStatus, errorBody(500, "boom"))
					return
				}
				respond(w, http.StatusCreated, strings.TrimSuffix(leaseBody, "}")+`,"token":"`+leaseToken+`"}`)
			})
			path := filepath.Join(t.TempDir(), "token")
			stdout, stderr, err := h.run(t, "sandbox", "create", "widgets", "--token-file", path)
			if code := ExitCode(err); code != 1 {
				t.Fatalf("exit code = %d (%v); want 1", code, err)
			}
			if reqs := h.requests(); len(reqs) != 2 || reqs[1].Method != http.MethodDelete || reqs[1].Path != "/api/v1/workspaces/widgets/sandboxes/"+leaseID {
				t.Fatalf("requests = %+v; want the POST then a DELETE of the lease", reqs)
			}
			msg := errorMessage(t, stdout)
			if !strings.Contains(msg, path) || !strings.Contains(msg, "disk full") || !strings.Contains(msg, tc.want) || strings.Contains(msg, tc.reject) {
				t.Errorf("message = %q; want the file, the cause and %q", msg, tc.want)
			}
			if strings.Contains(stdout+stderr, leaseToken) {
				t.Errorf("the token reached stdout or stderr: %s %s", stdout, stderr)
			}
		})
	}
}

// Label and token-file mistakes exit 2 before any request; a hub refusal
// exits 1 and leaves the token file empty.
func TestSandboxCreate_RefusalsAndUsageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"label without =": {[]string{"--label", "novalue"}, "missing '='"},
		"repeated label":  {[]string{"--label", "k=1", "--label", "k=2"}, `label "k" is given more than once`},
		"unwritable file": {[]string{"--token-file", filepath.Join(t.TempDir(), "no", "dir", "token")}, "cannot open the token file"},
		"file is a dir":   {[]string{"--token-file", t.TempDir()}, "cannot open the token file"},
	} {
		t.Run(name, func(t *testing.T) {
			h := leaseHub(t)
			stdout, _, err := h.run(t, append([]string{"sandbox", "create", "widgets"}, tc.args...)...)
			assertUsageError(t, h, stdout, err, tc.want)
		})
	}

	h := newHub(t, http.StatusUnprocessableEntity, errorBody(422, "required secrets not found: GIT_PAT"))
	path := filepath.Join(t.TempDir(), "token")
	stdout, _, err := h.run(t, "sandbox", "create", "widgets", "--token-file", path)
	assertEnvelopeOnly(t, stdout, err, "required secrets not found: GIT_PAT")
	if got, _ := os.ReadFile(path); len(got) != 0 {
		t.Errorf("the token file holds %q after a refused create", got)
	}
}

// Hub errors on the other subcommands exit 1 with the envelope alone, and a
// wrong number of arguments exits 2 without a request.
func TestSandbox_HubErrorsAndArgumentCounts(t *testing.T) {
	for _, args := range [][]string{{"profiles"}, {"list", "w"}, {"get", "w", "x"}, {"destroy", "w", "x"}} {
		h := newHub(t, http.StatusNotFound, errorBody(404, "sandbox lease not found"))
		stdout, _, err := h.run(t, append([]string{"sandbox"}, args...)...)
		assertEnvelopeOnly(t, stdout, err, "sandbox lease not found")
	}
	for _, args := range [][]string{{"profiles", "extra"}, {"list"}, {"list", "a", "b"}, {"get", "w"}, {"create"}, {"destroy", "w"}} {
		h := leaseHub(t)
		stdout, _, err := h.run(t, append([]string{"sandbox"}, args...)...)
		assertUsageError(t, h, stdout, err, "")
	}
}
