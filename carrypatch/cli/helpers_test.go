package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// call is one request the mock hub received. Path is the escaped path under
// /api/v1; Body is nil when the request carried none.
type call struct {
	Method, Path string
	Body         map[string]any
}

type reply struct {
	status int
	body   any
}

// mockHub answers requests from a table keyed by "METHOD /path" (the path
// under /api/v1) and records every request. Unknown routes get a 404 envelope.
type mockHub struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []call
	replies map[string]reply
}

func newMockHub(t *testing.T) *mockHub {
	t.Helper()
	h := &mockHub{replies: map[string]reply{}}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1")
		c := call{Method: r.Method, Path: path}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &c.Body)
		}
		h.mu.Lock()
		h.calls = append(h.calls, c)
		rep, ok := h.replies[r.Method+" "+path]
		h.mu.Unlock()
		if !ok {
			rep = reply{http.StatusNotFound, map[string]any{"error": map[string]any{"code": 404, "message": "no route for " + r.Method + " " + path}}}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		if rep.body != nil {
			_ = json.NewEncoder(w).Encode(rep.body)
		}
	}))
	t.Cleanup(h.Close)
	return h
}

// reply registers the answer for method and path (under /api/v1).
func (h *mockHub) reply(method, path string, status int, body any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replies[method+" "+path] = reply{status, body}
}

func (h *mockHub) requests() []call {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]call(nil), h.calls...)
}

// run executes afc with args against the hub the way afc wires these
// commands (apikit's root with credential resolution, patch-status under
// workspace), with $HOME isolated from the user's config. It returns stdout,
// stderr and the exit error.
func run(t *testing.T, hub *mockHub, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ak"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ak", "config.toml"), []byte("endpoint_url = \"\"\nuser_id = \"\"\napi_key = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := apikit.RootCommand()
	root.Use = "afc"
	workspace := &cobra.Command{Use: "workspace", Short: "Manage workspaces"}
	workspace.AddCommand(PatchStatusCmd())
	root.AddCommand(PatchCmd(), RebuildCmd(), RerereCmd(), workspace)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--endpoint-url", hub.URL, "--api-key", "test-api-key"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// jsonDocs decodes every JSON object written to stdout, in order.
func jsonDocs(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var docs []map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("stdout is not a sequence of JSON objects: %v\n%s", err, stdout)
		}
		docs = append(docs, m)
	}
	return docs
}

// errorEnvelope returns code and message of the error envelope that is the
// last JSON object on stdout.
func errorEnvelope(t *testing.T, stdout string) (code float64, message string) {
	t.Helper()
	docs := jsonDocs(t, stdout)
	if len(docs) == 0 {
		t.Fatal("no JSON on stdout")
	}
	e, ok := docs[len(docs)-1]["error"].(map[string]any)
	if !ok {
		t.Fatalf("stdout does not end with an error envelope:\n%s", stdout)
	}
	code, _ = e["code"].(float64)
	message, _ = e["message"].(string)
	return code, message
}

// expectCall asserts the hub received exactly one request, with the given
// method, path and decoded body.
func expectCall(t *testing.T, hub *mockHub, method, path string, body map[string]any) {
	t.Helper()
	calls := hub.requests()
	if len(calls) != 1 {
		t.Fatalf("hub received %d requests, want 1: %+v", len(calls), calls)
	}
	if c := calls[0]; c.Method != method || c.Path != path || !reflect.DeepEqual(c.Body, body) {
		t.Errorf("request = %s %s %v, want %s %s %v", c.Method, c.Path, c.Body, method, path, body)
	}
}

// roundTrip is a command that forwards one request and prints the hub's JSON
// answer unchanged, or the hub's error as an envelope with a non-zero exit.
type roundTrip struct {
	name   string
	args   []string
	method string
	path   string
	body   map[string]any
}

func checkRoundTrips(t *testing.T, trips []roundTrip) {
	t.Helper()
	for _, tt := range trips {
		t.Run(tt.name, func(t *testing.T) {
			hub := newMockHub(t)
			answer := map[string]any{"id": "x1", "echo": tt.name}
			hub.reply(tt.method, tt.path, http.StatusOK, answer)
			stdout, stderr, err := run(t, hub, tt.args...)
			if err != nil {
				t.Fatalf("exit error: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
			}
			expectCall(t, hub, tt.method, tt.path, tt.body)
			if docs := jsonDocs(t, stdout); len(docs) != 1 || !reflect.DeepEqual(docs[0], answer) {
				t.Errorf("stdout = %s, want the hub's answer %v", stdout, answer)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
		t.Run(tt.name+" api error", func(t *testing.T) {
			hub := newMockHub(t)
			hub.reply(tt.method, tt.path, http.StatusConflict, map[string]any{"error": map[string]any{"code": 409, "message": "hub says no"}})
			stdout, stderr, err := run(t, hub, tt.args...)
			if err == nil {
				t.Fatal("exit error is nil for a hub error")
			}
			if code, msg := errorEnvelope(t, stdout); code != 409 || msg != "hub says no" {
				t.Errorf("envelope = %v %q, want 409 \"hub says no\"", code, msg)
			}
			if strings.Contains(stdout+stderr, "Usage:") {
				t.Error("usage text printed for a hub error")
			}
		})
	}
}

// checkArgCounts asserts each argument list is refused before any request.
func checkArgCounts(t *testing.T, cases [][]string) {
	t.Helper()
	for _, args := range cases {
		hub := newMockHub(t)
		if _, _, err := run(t, hub, args...); err == nil {
			t.Errorf("%v: accepted; want an argument error", args)
		}
		if n := len(hub.requests()); n != 0 {
			t.Errorf("%v: %d requests sent, want none", args, n)
		}
	}
}
