package cli

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestIsTerminalStatus(t *testing.T) {
	for status, want := range map[string]bool{"completed": true, "failed": true, "dead_letter": true, "cancelled": true,
		"queued": false, "running": false, "": false} {
		if got := isTerminalStatus(status); got != want {
			t.Errorf("isTerminalStatus(%q) = %v, want %v", status, got, want)
		}
	}
}

// --wait prints the submitted job, then polls its status (the first poll at
// once, before any interval) until a terminal status, printing the final
// record and exiting non-zero unless the job completed. A poll failure or an
// answer without an id ends the wait with an error envelope.
func TestRebuildSubmit_WaitPollsUntilTerminal(t *testing.T) {
	queued := map[string]any{"id": "j1", "status": "queued"}
	tests := []struct {
		name         string
		submit       map[string]any // answer to the POST
		pollStatus   int
		poll         map[string]any // answer to the GET
		wantErr      bool
		wantFinal    map[string]any // the record printed after the poll, if any
		wantEnvelope string         // substring of the error envelope message, if any
		wantStderr   string
		wantPolls    int
	}{
		{"completed", queued, http.StatusOK, map[string]any{"id": "j1", "status": "completed"},
			false, map[string]any{"id": "j1", "status": "completed"}, "", "", 1},
		{"failed", queued, http.StatusOK, map[string]any{"id": "j1", "status": "failed", "error": "boom"},
			true, map[string]any{"id": "j1", "status": "failed", "error": "boom"}, "job ended with status failed", "Job ended with status failed.", 1},
		{"never terminal", queued, http.StatusOK, map[string]any{"id": "j1", "status": "running"},
			true, nil, "timed out after 400ms", "Timed out waiting for job to complete after 400ms", 1},
		{"poll error", queued, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": 500, "message": "status boom"}},
			true, nil, "status boom", "", 1},
		{"submit answer without id", map[string]any{"status": "queued"}, http.StatusOK, nil,
			true, nil, "response missing 'id' field", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newMockHub(t)
			hub.reply("POST", "/workspaces/ws/rebuild", http.StatusAccepted, tt.submit)
			hub.reply("GET", "/workspaces/ws/rebuilds/j1", tt.pollStatus, tt.poll)
			stdout, stderr, err := run(t, hub, "rebuild", "submit", "ws", "--wait", "--timeout", "400ms", "--poll-interval", "10s")

			if (err != nil) != tt.wantErr {
				t.Errorf("exit error = %v, want error: %v", err, tt.wantErr)
			}
			docs := jsonDocs(t, stdout)
			if len(docs) == 0 || !reflect.DeepEqual(docs[0], tt.submit) {
				t.Errorf("stdout does not start with the submit answer:\n%s", stdout)
			}
			if tt.wantFinal != nil && (len(docs) < 2 || !reflect.DeepEqual(docs[1], tt.wantFinal)) {
				t.Errorf("stdout lacks the final record %v:\n%s", tt.wantFinal, stdout)
			}
			if tt.wantEnvelope != "" {
				if _, msg := errorEnvelope(t, stdout); !strings.Contains(msg, tt.wantEnvelope) {
					t.Errorf("envelope message %q does not contain %q", msg, tt.wantEnvelope)
				}
			}
			if (tt.wantStderr == "" && stderr != "") || !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want %q", stderr, tt.wantStderr)
			}
			if polls := len(hub.requests()) - 1; polls != tt.wantPolls {
				t.Errorf("%d status polls, want %d", polls, tt.wantPolls)
			}
		})
	}
}
