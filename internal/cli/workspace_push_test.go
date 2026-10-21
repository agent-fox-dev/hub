package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// pushHub is a stub hub that records each request's escaped path and body,
// and what afc had written to stderr when the request arrived.
type pushHub struct {
	*httptest.Server
	stderr *syncBuffer

	mu       sync.Mutex
	paths    []string
	bodies   []string
	stderrAt []string
}

// syncBuffer is a bytes.Buffer safe for the command and the stub hub.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newPushHub(t *testing.T, status int, body string) *pushHub {
	t.Helper()
	h := &pushHub{stderr: &syncBuffer{}}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.paths = append(h.paths, r.URL.EscapedPath())
		h.bodies = append(h.bodies, string(sent))
		h.stderrAt = append(h.stderrAt, h.stderr.String())
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(h.Close)
	return h
}

// run runs afc with args against the hub and returns stdout and the error.
func (h *pushHub) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	setupTestEnv(t)
	cmd := BuildRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(h.stderr)
	cmd.SetArgs(append([]string{"--endpoint-url", h.URL, "--api-key", "test-api-key"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func (h *pushHub) requests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.paths)
}

const pushResponse = `{"branch":"fix/42-nil-map","sha":"0123456789abcdef0123456789abcdef01234567","remote":"origin","remote_url":"https://github.com/acme/widgets.git","result":"pushed"}`

// TS-38-46: afc workspace push sends the escaped path and {force:bool} and
// warns on stderr only with --force.
// Requirement: 38-REQ-9.1, 38-REQ-9.2
func TestWorkspacePushRequest_TS_38_46(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		force bool
	}{
		{[]string{"workspace", "push", "ws", "fix/42-nil-map"}, false},
		{[]string{"workspace", "push", "ws", "fix/42-nil-map", "--force"}, true},
	} {
		hub := newPushHub(t, http.StatusOK, pushResponse)
		if _, err := hub.run(t, tc.args...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if hub.requests() != 1 {
			t.Fatalf("%v: the hub received %d requests; want 1", tc.args, hub.requests())
		}
		if hub.paths[0] != "/api/v1/workspaces/ws/branches/fix%2F42-nil-map/push" {
			t.Errorf("path %s; want /api/v1/workspaces/ws/branches/fix%%2F42-nil-map/push", hub.paths[0])
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(hub.bodies[0]), &body); err != nil || !reflect.DeepEqual(body, map[string]any{"force": tc.force}) {
			t.Errorf("body %s (%v); want {\"force\": %v}", hub.bodies[0], err, tc.force)
		}
		lines := strings.Split(strings.TrimSpace(hub.stderr.String()), "\n")
		if !tc.force {
			if hub.stderr.String() != "" {
				t.Errorf("stderr %q without --force; want nothing", hub.stderr.String())
			}
			continue
		}
		if len(lines) != 1 || !strings.Contains(lines[0], "origin") || !strings.Contains(lines[0], "overwritten") {
			t.Errorf("stderr %q; want one warning that origin's branch may be overwritten", hub.stderr.String())
		}
		if hub.stderrAt[0] != hub.stderr.String() {
			t.Errorf("the warning was written after the request was sent")
		}
	}
}

// TS-38-47: On a 200 answer afc prints the response as one JSON document and
// exits 0.
// Requirement: 38-REQ-9.3
func TestWorkspacePushPrintsResponse_TS_38_47(t *testing.T) {
	hub := newPushHub(t, http.StatusOK, pushResponse)
	stdout, err := hub.run(t, "workspace", "push", "ws", "feat")
	if err != nil || ExitCode(err) != 0 {
		t.Fatalf("exit %d: %v", ExitCode(err), err)
	}
	var want any
	_ = json.Unmarshal([]byte(pushResponse), &want)
	if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout %v; want %v", got, want)
	}
}

// TS-38-48: On a hub error afc prints the error envelope and exits 1, with
// non_fast_forward visible.
// Requirement: 38-REQ-9.4
func TestWorkspacePushHubError_TS_38_48(t *testing.T) {
	hub := newPushHub(t, http.StatusConflict,
		`{"error":{"code":409,"message":"non_fast_forward: origin's feat is at abc123, which is not in the workspace's branch; push with force to overwrite it","error_type":"non_fast_forward"}}`)
	stdout, err := hub.run(t, "workspace", "push", "ws", "feat")
	if code := ExitCode(err); code != 1 {
		t.Errorf("exit %d; want 1 (%v)", code, err)
	}
	var doc map[string]map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil || doc["error"] == nil {
		t.Fatalf("stdout %q; want apikit's error envelope (%v)", stdout, err)
	}
	if code, _ := doc["error"]["code"].(float64); code != 409 {
		t.Errorf("code %v; want 409", doc["error"]["code"])
	}
	if msg, _ := doc["error"]["message"].(string); !strings.Contains(msg, "non_fast_forward") {
		t.Errorf("message %q does not name non_fast_forward", msg)
	}
}

// TS-38-49: A client-side failure exits 2.
// Requirement: 38-REQ-9.5
func TestWorkspacePushClientError_TS_38_49(t *testing.T) {
	setupTestEnv(t)
	refused := httptest.NewServer(http.NotFoundHandler())
	url := refused.URL
	refused.Close()
	stdout, _, err := runRootCmd(t, "--endpoint-url", url, "--api-key", "k", "workspace", "push", "ws", "feat")
	if code := ExitCode(err); code != 2 {
		t.Errorf("unreachable hub: exit %d; want 2 (%v)", code, err)
	}
	if !strings.Contains(stdout, `"error"`) {
		t.Errorf("unreachable hub: stdout %q; want the client error reported", stdout)
	}

	setupTestEnv(t)
	t.Setenv("AK_ENDPOINT_URL", "")
	t.Setenv("AK_API_KEY", "")
	_, _, err = runRootCmd(t, "workspace", "push", "ws", "feat")
	if code := ExitCode(err); code != 2 {
		t.Errorf("no configuration: exit %d; want 2 (%v)", code, err)
	}
}

// TS-38-50: Fewer or more than two arguments exit 2 without contacting hub.
// Requirement: 38-REQ-9.6
func TestWorkspacePushArgs_TS_38_50(t *testing.T) {
	hub := newPushHub(t, http.StatusOK, pushResponse)
	for _, args := range [][]string{{}, {"ws"}, {"ws", "a", "b"}} {
		_, err := hub.run(t, append([]string{"workspace", "push"}, args...)...)
		if code := ExitCode(err); code != 2 {
			t.Errorf("%v: exit %d; want 2 (%v)", args, code, err)
		}
	}
	if n := hub.requests(); n != 0 {
		t.Errorf("the hub received %d requests; want none", n)
	}
}
