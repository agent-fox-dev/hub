package cli

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestPatch_ForwardsRequestsAndPrintsAnswers(t *testing.T) {
	checkRoundTrips(t, []roundTrip{
		{"add", []string{"patch", "add", "ws", "--branch", "feature/x"},
			"POST", "/workspaces/ws/patches", map[string]any{"branch_name": "feature/x"}},
		{"add with every option", []string{"patch", "add", "ws", "--branch", "feature/x", "--position", "2",
			"--upstream-pr", "https://gh/pr/1", "--description", "why", "--skip-branch-check", "--if-not-exists"},
			"POST", "/workspaces/ws/patches", map[string]any{"branch_name": "feature/x", "position": 2.0,
				"upstream_pr_url": "https://gh/pr/1", "description": "why", "skip_branch_check": true, "if_not_exists": true}},
		{"list", []string{"patch", "list", "ws"}, "GET", "/workspaces/ws/patches", nil},
		{"slug is escaped", []string{"patch", "list", "a/b"}, "GET", "/workspaces/a%2Fb/patches", nil},
		{"reorder", []string{"patch", "reorder", "ws", "p1", "p2"},
			"POST", "/workspaces/ws/patches/reorder", map[string]any{"patch_ids": []any{"p1", "p2"}}},
		{"update sends only the flags given", []string{"patch", "update", "ws", "p1", "--status", "disabled"},
			"PATCH", "/workspaces/ws/patches/p1", map[string]any{"status": "disabled"}},
		{"update sends zero values when the flag is given", []string{"patch", "update", "ws", "p1", "--description", "", "--position", "0", "--upstream-pr", ""},
			"PATCH", "/workspaces/ws/patches/p1", map[string]any{"description": "", "position": 0.0, "upstream_pr_url": ""}},
		{"restore", []string{"patch", "restore", "ws", "p1"}, "POST", "/workspaces/ws/patches/p1/restore", nil},
		{"reset-to-origin", []string{"patch", "reset-to-origin", "ws", "p1"}, "POST", "/workspaces/ws/patches/p1/reset-to-origin", nil},
	})
}

// The hub answers 204, so the command prints its own status object and a
// confirmation on stderr.
func TestPatchRemove_PrintsSyntheticStatus(t *testing.T) {
	hub := newMockHub(t)
	hub.reply("DELETE", "/workspaces/ws/patches/p1", http.StatusNoContent, nil)
	stdout, stderr, err := run(t, hub, "patch", "remove", "ws", "p1")
	if err != nil {
		t.Fatalf("exit error: %v", err)
	}
	expectCall(t, hub, "DELETE", "/workspaces/ws/patches/p1", nil)
	want := map[string]any{"status": "removed", "patch_id": "p1"}
	if docs := jsonDocs(t, stdout); len(docs) != 1 || !reflect.DeepEqual(docs[0], want) {
		t.Errorf("stdout = %s, want %v", stdout, want)
	}
	if !strings.Contains(stderr, "Patch 'p1' has been removed.") {
		t.Errorf("stderr = %q", stderr)
	}
}

// Missing required input is a code-2 envelope, reported before any request.
func TestPatch_RejectsIncompleteInput(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{"add without --branch", []string{"patch", "add", "ws"}, "--branch flag is required"},
		{"update without fields", []string{"patch", "update", "ws", "p1"}, "at least one of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newMockHub(t)
			stdout, _, err := run(t, hub, tt.args...)
			if err == nil {
				t.Fatal("exit error is nil")
			}
			if code, msg := errorEnvelope(t, stdout); code != 2 || !strings.Contains(msg, tt.wantMsg) {
				t.Errorf("envelope = %v %q, want 2 and %q", code, msg, tt.wantMsg)
			}
			if n := len(hub.requests()); n != 0 {
				t.Errorf("%d requests sent, want none", n)
			}
		})
	}
}

func TestPatch_RejectsWrongArgumentCount(t *testing.T) {
	checkArgCounts(t, [][]string{
		{"patch", "add"}, {"patch", "list"}, {"patch", "remove", "ws"}, {"patch", "reorder", "ws"},
		{"patch", "update", "ws"}, {"patch", "restore", "ws"}, {"patch", "reset-to-origin", "ws", "p1", "extra"},
	})
}
