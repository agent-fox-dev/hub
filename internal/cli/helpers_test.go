package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The tests of this package run the afc command tree in-process against a
// stub of the hub API and check the request it sent and the JSON it printed.
// The stub, the runner and the decoders live here; e2e_test.go holds the two
// tests that run the real hub routes instead.

const testAPIKey = "test-api-key"

// request is one request the stub hub received.
type request struct {
	Method string
	Path   string // escaped, as sent on the wire
	Query  string // raw query string
	Auth   string // the Authorization header
	Body   string
}

// hub is a stub of the hub API. It records every request and answers each
// through its handler.
type hub struct {
	*httptest.Server

	mu   sync.Mutex
	reqs []request
}

// newHub returns a hub that answers every request with status and body.
func newHub(t *testing.T, status int, body string) *hub {
	t.Helper()
	return newHubFunc(t, func(w http.ResponseWriter, _ request) { respond(w, status, body) })
}

// newHubFunc returns a hub whose answers handler decides per request.
func newHubFunc(t *testing.T, handler func(w http.ResponseWriter, r request)) *hub {
	t.Helper()
	h := &hub{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := request{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), string(body)}
		h.mu.Lock()
		h.reqs = append(h.reqs, rec)
		h.mu.Unlock()
		handler(w, rec)
	}))
	t.Cleanup(h.Close)
	return h
}

// respond writes a JSON answer; an empty body writes only the status.
func respond(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// errorBody is the hub's error envelope with code and message.
func errorBody(code int, message string) string {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message}})
	return string(raw)
}

func (h *hub) requests() []request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]request(nil), h.reqs...)
}

// only returns the one request the hub received and fails when there was
// not exactly one.
func (h *hub) only(t *testing.T) request {
	t.Helper()
	reqs := h.requests()
	if len(reqs) != 1 {
		t.Fatalf("the hub received %d requests; want 1: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

// none fails when the hub received any request.
func (h *hub) none(t *testing.T) {
	t.Helper()
	if reqs := h.requests(); len(reqs) != 0 {
		t.Errorf("the hub received %+v; want no request", reqs)
	}
}

// run runs afc against the hub with the test API key.
func (h *hub) run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return h.runStdin(t, "", args...)
}

// runStdin is run with stdin.
func (h *hub) runStdin(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return afc(t, h.URL, testAPIKey, stdin, args...)
}

// afc runs the root command against endpoint with apiKey (none when empty)
// and stdin, in a HOME of its own, and returns what it printed.
func afc(t *testing.T, endpoint, apiKey, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	isolateHome(t)
	full := []string{"--endpoint-url", endpoint}
	if apiKey != "" {
		full = append(full, "--api-key", apiKey)
	}
	return execute(t, stdin, append(full, args...)...)
}

// execute runs the root command as built and returns what it printed.
func execute(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := BuildRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// isolateHome points HOME at a directory with an empty apikit config, so
// that the user's own configuration never reaches a test.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ak")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("endpoint_url = \"\"\nuser_id = \"\"\napi_key = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// decodeDocuments decodes the JSON documents stdout holds, in order.
func decodeDocuments(t *testing.T, stdout string) []any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var docs []any
	for dec.More() {
		var doc any
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("stdout is not a sequence of JSON documents: %v\n%q", err, stdout)
		}
		docs = append(docs, doc)
	}
	return docs
}

// decodeOneDocument decodes stdout, which must hold exactly one JSON document.
func decodeOneDocument(t *testing.T, stdout string) any {
	t.Helper()
	docs := decodeDocuments(t, stdout)
	if len(docs) != 1 {
		t.Fatalf("stdout holds %d JSON documents; want 1: %q", len(docs), stdout)
	}
	return docs[0]
}

// marshalJSON encodes v as a JSON text.
func marshalJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// decodeJSON decodes raw, a JSON text of a test or a request body.
func decodeJSON(t *testing.T, raw string) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%q", err, raw)
	}
	return doc
}

// errorMessage returns the message of the first error envelope among the
// documents stdout holds, or "" when there is none.
func errorMessage(t *testing.T, stdout string) string {
	t.Helper()
	for _, doc := range decodeDocuments(t, stdout) {
		m, _ := doc.(map[string]any)
		if e, ok := m["error"].(map[string]any); ok {
			msg, _ := e["message"].(string)
			return msg
		}
	}
	return ""
}

// assertEnvelopeOnly checks that stdout holds the hub's error envelope with
// message and nothing else, and that err maps to exit code 1.
func assertEnvelopeOnly(t *testing.T, stdout string, err error, message string) {
	t.Helper()
	if code := ExitCode(err); code != 1 {
		t.Errorf("exit code = %d (%v); want 1 for an API error", code, err)
	}
	doc, ok := decodeOneDocument(t, stdout).(map[string]any)
	if !ok || len(doc) != 1 || doc["error"] == nil {
		t.Fatalf("stdout = %q; want the error envelope alone", stdout)
	}
	if got := errorMessage(t, stdout); got != message {
		t.Errorf("error message = %q; want %q", got, message)
	}
}

// assertUsageError checks that err is a usage error (exit code 2) whose
// message, on stdout or in err, names want, and that the hub saw nothing.
func assertUsageError(t *testing.T, h *hub, stdout string, err error, want string) {
	t.Helper()
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("exit code = %d (%v); want 2", ExitCode(err), err)
	}
	if want != "" && !strings.Contains(stdout, want) && !strings.Contains(err.Error(), want) {
		t.Errorf("stdout = %q, err = %v; want %q named", stdout, err, want)
	}
	h.none(t)
}
