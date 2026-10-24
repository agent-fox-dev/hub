package cli

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestRebuild_ForwardsRequestsAndPrintsAnswers(t *testing.T) {
	checkRoundTrips(t, []roundTrip{
		{"submit without options sends no body", []string{"rebuild", "submit", "ws"}, "POST", "/workspaces/ws/rebuild", nil},
		{"submit with options", []string{"rebuild", "submit", "ws", "--strategy", "merge", "--fail-mode", "continue"},
			"POST", "/workspaces/ws/rebuild", map[string]any{"strategy": "merge", "fail_mode": "continue"}},
		{"list", []string{"rebuild", "list", "ws"}, "GET", "/workspaces/ws/rebuilds", nil},
		{"status", []string{"rebuild", "status", "ws", "j1"}, "GET", "/workspaces/ws/rebuilds/j1", nil},
		{"preview", []string{"rebuild", "preview", "ws"}, "GET", "/workspaces/ws/rebuild-preview", nil},
		{"requeue", []string{"rebuild", "requeue", "ws", "j1"}, "POST", "/workspaces/ws/rebuilds/j1/requeue", nil},
		{"rollback", []string{"rebuild", "rollback", "ws", "j1"}, "POST", "/workspaces/ws/rebuilds/j1/rollback", nil},
	})
}

// The hub answers 204, so the command prints its own status object and a
// confirmation on stderr.
func TestRebuildCancel_PrintsSyntheticStatus(t *testing.T) {
	hub := newMockHub(t)
	hub.reply("DELETE", "/workspaces/ws/rebuilds/j1", http.StatusNoContent, nil)
	stdout, stderr, err := run(t, hub, "rebuild", "cancel", "ws", "j1")
	if err != nil {
		t.Fatalf("exit error: %v", err)
	}
	expectCall(t, hub, "DELETE", "/workspaces/ws/rebuilds/j1", nil)
	want := map[string]any{"status": "cancelled", "rebuild_id": "j1"}
	if docs := jsonDocs(t, stdout); len(docs) != 1 || !reflect.DeepEqual(docs[0], want) {
		t.Errorf("stdout = %s, want %v", stdout, want)
	}
	if !strings.Contains(stderr, "Rebuild job 'j1' has been cancelled.") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRebuild_RejectsWrongArgumentCount(t *testing.T) {
	checkArgCounts(t, [][]string{
		{"rebuild", "submit"}, {"rebuild", "list"}, {"rebuild", "status", "ws"}, {"rebuild", "preview"},
		{"rebuild", "cancel", "ws"}, {"rebuild", "requeue", "ws"}, {"rebuild", "rollback", "ws"},
	})
}
