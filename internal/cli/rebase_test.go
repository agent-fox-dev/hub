package cli

import (
	"net/http"
	"reflect"
	"testing"
)

// rebase submit sends the target ref and the comma-separated branches as a
// list, and prints the answer.
func TestRebaseSubmit_SendsTheBranches(t *testing.T) {
	const answer = `{"id":"r-1","status":"queued"}`
	h := newHub(t, http.StatusAccepted, answer)
	stdout, _, err := h.run(t, "rebase", "submit", "a/b", "--target-ref", "main", "--branches", "f1,f2")
	if err != nil {
		t.Fatalf("rebase submit: %v", err)
	}
	req := h.only(t)
	want := map[string]any{"target_ref": "main", "branches": []any{"f1", "f2"}}
	if req.Method != http.MethodPost || req.Path != "/api/v1/workspaces/a%2Fb/rebase" || !reflect.DeepEqual(decodeJSON(t, req.Body), any(want)) {
		t.Errorf("request = %+v; want POST /api/v1/workspaces/a%%2Fb/rebase with %v", req, want)
	}
	if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, decodeJSON(t, answer)) {
		t.Errorf("stdout = %q; want the hub's answer", stdout)
	}
}

// Both flags and the slug are required, and a hub error is reported with
// exit 1.
func TestRebaseSubmit_UsageAndHubErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"ws", "--branches", "f"}, "--target-ref flag is required"},
		{[]string{"ws", "--target-ref", "main"}, "--branches flag is required"},
		{nil, ""},
	} {
		h := newHub(t, http.StatusAccepted, `{}`)
		stdout, _, err := h.run(t, append([]string{"rebase", "submit"}, tc.args...)...)
		assertUsageError(t, h, stdout, err, tc.want)
	}

	h := newHub(t, http.StatusInternalServerError, errorBody(500, "rebase failed"))
	stdout, _, err := h.run(t, "rebase", "submit", "ws", "--target-ref", "main", "--branches", "f")
	assertEnvelopeOnly(t, stdout, err, "rebase failed")
}
