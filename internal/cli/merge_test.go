package cli

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const mergeJob = `{"id":"m-1","workspace_slug":"ws","target_branch":"main","source_ref":"feature/a","status":"queued"}`

// Each merge subcommand sends its route and prints the answer; cancel prints
// a status document of its own, since the hub answers 204.
func TestMerge_CommandsSendTheirRoutes(t *testing.T) {
	const base = "/api/v1/workspaces/a%2Fb/merges"
	for _, tc := range []struct {
		args         []string
		method, path string
		body         string
		stdout       string
	}{
		{[]string{"submit", "a/b", "--target", "main", "--source", "feature/a"}, http.MethodPost, base,
			`{"target_branch":"main","source_ref":"feature/a"}`, mergeJob},
		{[]string{"list", "a/b"}, http.MethodGet, base, "", mergeJob},
		{[]string{"status", "a/b", "m 1"}, http.MethodGet, base + "/m%201", "", mergeJob},
		{[]string{"requeue", "a/b", "m-1"}, http.MethodPost, base + "/m-1/requeue", "", mergeJob},
		{[]string{"cancel", "a/b", "m-1"}, http.MethodDelete, base + "/m-1", "", `{"status":"cancelled","merge_id":"m-1"}`},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			h := newHub(t, http.StatusOK, mergeJob)
			stdout, stderr, err := h.run(t, append([]string{"merge"}, tc.args...)...)
			if err != nil {
				t.Fatalf("merge %v: %v", tc.args, err)
			}
			req := h.only(t)
			if req.Method != tc.method || req.Path != tc.path {
				t.Errorf("request = %s %s; want %s %s", req.Method, req.Path, tc.method, tc.path)
			}
			if tc.body == "" && req.Body != "" || tc.body != "" && !reflect.DeepEqual(decodeJSON(t, req.Body), decodeJSON(t, tc.body)) {
				t.Errorf("body = %q; want %q", req.Body, tc.body)
			}
			if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, decodeJSON(t, tc.stdout)) {
				t.Errorf("stdout = %q; want %s", stdout, tc.stdout)
			}
			if tc.args[0] == "cancel" && !strings.Contains(stderr, "Merge job 'm-1' has been cancelled.") {
				t.Errorf("stderr = %q; want the confirmation", stderr)
			}
		})
	}
}

// submit needs --target and --source, and every subcommand its arguments,
// before anything is sent.
func TestMerge_UsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"submit", "ws", "--source", "f"}, "--target flag is required"},
		{[]string{"submit", "ws", "--target", "main"}, "--source flag is required"},
		{[]string{"submit"}, ""},
		{[]string{"list"}, ""},
		{[]string{"status", "ws"}, ""},
		{[]string{"cancel", "ws"}, ""},
		{[]string{"requeue", "ws", "m", "extra"}, ""},
	} {
		h := newHub(t, http.StatusOK, mergeJob)
		stdout, _, err := h.run(t, append([]string{"merge"}, tc.args...)...)
		assertUsageError(t, h, stdout, err, tc.want)
	}
}

// A hub error exits 1 with the hub's envelope as the only document on stdout.
func TestMerge_HubErrorsAreReported(t *testing.T) {
	for _, args := range [][]string{
		{"submit", "ws", "--target", "main", "--source", "f"}, {"list", "ws"}, {"status", "ws", "m"}, {"cancel", "ws", "m"}, {"requeue", "ws", "m"},
	} {
		h := newHub(t, http.StatusConflict, errorBody(409, "merge job is not dead-lettered"))
		stdout, _, err := h.run(t, append([]string{"merge"}, args...)...)
		assertEnvelopeOnly(t, stdout, err, "merge job is not dead-lettered")
	}
}
