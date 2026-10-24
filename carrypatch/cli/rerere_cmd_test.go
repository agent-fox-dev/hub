package cli

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestRerereList_ForwardsRequestAndPrintsAnswer(t *testing.T) {
	checkRoundTrips(t, []roundTrip{
		{"list", []string{"rerere", "list", "ws"}, "GET", "/workspaces/ws/rerere", nil},
	})
}

// The pathspec keeps its slashes in the route (each element encoded); the
// hub answers 204, so the command prints its own status object and a
// confirmation on stderr.
func TestRerereForget_KeepsPathspecSlashesAndPrintsSyntheticStatus(t *testing.T) {
	hub := newMockHub(t)
	hub.reply("DELETE", "/workspaces/ws/rerere/src/a%20b.go", http.StatusNoContent, nil)
	stdout, stderr, err := run(t, hub, "rerere", "forget", "ws", "src/a b.go")
	if err != nil {
		t.Fatalf("exit error: %v\n%s", err, stdout)
	}
	expectCall(t, hub, "DELETE", "/workspaces/ws/rerere/src/a%20b.go", nil)
	want := map[string]any{"status": "forgotten", "pathspec": "src/a b.go"}
	if docs := jsonDocs(t, stdout); len(docs) != 1 || !reflect.DeepEqual(docs[0], want) {
		t.Errorf("stdout = %s, want %v", stdout, want)
	}
	if !strings.Contains(stderr, "Rerere resolution for 'src/a b.go' has been forgotten.") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRerere_RejectsWrongArgumentCount(t *testing.T) {
	checkArgCounts(t, [][]string{{"rerere", "list"}, {"rerere", "forget", "ws"}})
}
