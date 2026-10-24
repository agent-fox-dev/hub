package cli

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// jobHub answers a submit with a queued job and each poll with the next of
// statuses, the last one repeated; the job record is `{"id":"job-1",...}`.
func jobHub(t *testing.T, statuses ...string) *hub {
	t.Helper()
	var mu sync.Mutex
	polls := 0
	return newHubFunc(t, func(w http.ResponseWriter, r request) {
		if r.Method == http.MethodPost {
			respond(w, http.StatusAccepted, `{"id":"job-1","status":"queued"}`)
			return
		}
		mu.Lock()
		i := min(polls, len(statuses)-1)
		polls++
		mu.Unlock()
		respond(w, http.StatusOK, `{"id":"job-1","status":"`+statuses[i]+`"}`)
	})
}

// The --wait loop polls the job as soon as it is submitted, prints the submit
// response and the final record, and stops at the first terminal status.
func TestWait_PollsImmediatelyUntilTheJobCompletes(t *testing.T) {
	h := jobHub(t, "running", "completed")
	start := time.Now()
	stdout, stderr, err := h.run(t, "merge", "submit", "ws", "--target", "main", "--source", "feat",
		"--wait", "--poll-interval", "50ms", "--timeout", "10s")
	if err != nil {
		t.Fatalf("merge submit --wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; the first poll must not wait for the interval", elapsed)
	}
	reqs := h.requests()
	if len(reqs) != 3 || reqs[1].Method != http.MethodGet || reqs[1].Path != "/api/v1/workspaces/ws/merges/job-1" {
		t.Errorf("requests = %+v; want the submit and two polls of /api/v1/workspaces/ws/merges/job-1", reqs)
	}
	docs := decodeDocuments(t, stdout)
	if len(docs) != 2 || docs[0].(map[string]any)["status"] != "queued" || docs[1].(map[string]any)["status"] != "completed" {
		t.Errorf("stdout = %q; want the submit response then the completed record", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q; want nothing", stderr)
	}
}

// A job that ends failed, dead_letter or cancelled is printed and exits 1
// with the status named on stderr.
func TestWait_NonCompletedTerminalStatusExitsOne(t *testing.T) {
	for _, status := range []string{"failed", "dead_letter", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			h := jobHub(t, status)
			stdout, stderr, err := h.run(t, "merge", "submit", "ws", "--target", "main", "--source", "feat",
				"--wait", "--poll-interval", "50ms")
			if code := ExitCode(err); code != 1 || IsDivergedError(err) {
				t.Errorf("exit code = %d (%v); want 1", code, err)
			}
			if !strings.Contains(stdout, `"status": "`+status+`"`) {
				t.Errorf("stdout = %q; want the final record", stdout)
			}
			if want := "Job ended with status " + status + "."; !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q; want %q", stderr, want)
			}
		})
	}
}

// When the job stays non-terminal past --timeout the wait ends with exit 1
// and a timeout message on stderr.
func TestWait_TimesOut(t *testing.T) {
	h := jobHub(t, "running")
	_, stderr, err := h.run(t, "merge", "submit", "ws", "--target", "main", "--source", "feat",
		"--wait", "--poll-interval", "20ms", "--timeout", "150ms")
	if code := ExitCode(err); code != 1 {
		t.Errorf("exit code = %d (%v); want 1", code, err)
	}
	if !strings.Contains(stderr, "Timed out waiting for job to complete after 150ms") {
		t.Errorf("stderr = %q; want the timeout message", stderr)
	}
	if n := len(h.requests()); n < 2 {
		t.Errorf("the hub received %d requests; want the submit and at least one poll", n)
	}
}

// A hub error while polling ends the wait with that error; a submit response
// without an id cannot be polled.
func TestWait_StopsOnAPollErrorOrAMissingJobID(t *testing.T) {
	h := newHubFunc(t, func(w http.ResponseWriter, r request) {
		if r.Method == http.MethodPost {
			respond(w, http.StatusAccepted, `{"id":"job-1","status":"queued"}`)
			return
		}
		respond(w, http.StatusInternalServerError, errorBody(500, "boom"))
	})
	stdout, _, err := h.run(t, "merge", "submit", "ws", "--target", "main", "--source", "feat", "--wait")
	if code := ExitCode(err); code != 1 || errorMessage(t, stdout) != "boom" {
		t.Errorf("exit code = %d, stdout = %q; want 1 and the hub's message", code, stdout)
	}

	h = newHub(t, http.StatusAccepted, `{"status":"queued"}`)
	stdout, _, err = h.run(t, "merge", "submit", "ws", "--target", "main", "--source", "feat", "--wait")
	if err == nil || !strings.Contains(errorMessage(t, stdout), "missing 'id'") {
		t.Errorf("err = %v, stdout = %q; want the missing id reported", err, stdout)
	}
	if n := len(h.requests()); n != 1 {
		t.Errorf("the hub received %d requests; want the submit only", n)
	}
}
