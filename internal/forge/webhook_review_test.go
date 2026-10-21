package forge

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// These tests came out of the review of the finished branch; each of the first five failed
// against the code as it was first delivered.

// failingWriter is a response writer whose body cannot be written, as a connection that the
// forge has already closed.
type failingWriter struct {
	header http.Header
	status int
}

func (w *failingWriter) Header() http.Header       { return w.header }
func (w *failingWriter) WriteHeader(status int)    { w.status = status }
func (w *failingWriter) Write([]byte) (int, error) { return 0, errors.New("write: broken pipe") }
func (w *failingWriter) Flush()                    {}

// A delivery whose answer cannot be written is recorded as relayed, so its redelivery is a
// duplicate: its events must be relayed all the same, or they are lost.
// Requirement: 32-REQ-8.6
func TestIngressRelaysTheEventsWhenTheAnswerCannotBeWritten(t *testing.T) {
	logs := captureLog(t)
	e, em := newIngressEnv(t)
	d := labeledDelivery("gh-1")
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", strings.NewReader(d.body))
	req.Header = d.header.Clone()
	w := &failingWriter{header: http.Header{}}
	e.echo.ServeHTTP(w, req)

	if got := eventTypes(em.recorded()); !reflect.DeepEqual(got, []string{audit.EventForgeIssueLabeled}) {
		t.Errorf("events = %v; want the one of the delivery", got)
	}
	if rows := e.deliveryRows(); len(rows) != 1 || rows[0].outcome != "relayed" {
		t.Errorf("rows = %+v; want one relayed row", rows)
	}
	if !strings.Contains(logs.String(), "could not be written") {
		t.Errorf("the log = %q; want the failed write said", logs)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", d)); outcome != "duplicate" {
		t.Errorf("the redelivery: outcome = %q; want duplicate", outcome)
	}
}

// A workspace whose stored secret is empty has none, for GET /forge as for the ingress.
// Requirement: 32-REQ-2.5
func TestForgeStatusTreatsAnEmptySecretAsNone(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "blank")
	if _, err := e.db.Exec(`INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at)
		VALUES ('workspace', 'blank', 'FORGE_WEBHOOK_SECRET', '', '2026-10-21T09:00:00Z', '2026-10-21T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if block, _ := getWebhookBlock(t, e, "blank"); block.Configured {
		t.Errorf("webhook = %+v; want not configured: the ingress answers 404 for this secret", block)
	}
}

// A delivery that would relay nothing is ignored, whichever repository it names; one that would
// relay events about a repository that is not the workspace's is a repository_mismatch.
// Requirement: 32-REQ-5.2, 32-REQ-5.5, 32-REQ-7.3
func TestIngressIgnoresWhatItWouldNotRelayWhateverTheRepository(t *testing.T) {
	e, em := newIngressEnv(t)
	foreign := func(body string) string { return strings.ReplaceAll(body, "acme/widgets", "other/repo") }
	for i, tc := range []struct {
		name string
		d    signedDelivery
		want string
	}{
		{"a note on a commit", gitlabDelivery(ingressSecret, "d-1", "Note Hook", foreign(noteHook("Commit", "", map[string]any{"commit": map[string]any{"id": "abc"}}))), "ignored"},
		{"an issue update without a change", gitlabDelivery(ingressSecret, "d-2", "Issue Hook", foreign(gitlabUpdateHook(map[string]any{}))), "ignored"},
		{"a merge request update without an oldrev", gitlabDelivery(ingressSecret, "d-3", "Merge Request Hook", foreign(mergeRequestHook("update", nil))), "ignored"},
		{"a GitHub ping", githubDelivery(ingressSecret, "d-4", "ping", foreign(`{"zen":"x","repository":{"full_name":"acme/widgets"}}`)), "ignored"},
		{"a label change", gitlabDelivery(ingressSecret, "d-5", "Issue Hook", foreign(gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"a"}, []string{"b"})}, "b"))), "repository_mismatch"},
		{"a merge request update with an oldrev", gitlabDelivery(ingressSecret, "d-6", "Merge Request Hook", foreign(mergeRequestHook("update", map[string]any{"oldrev": "abc"}))), "repository_mismatch"},
	} {
		if outcome := outcomeOf(t, e.deliver("widgets", tc.d)); outcome != tc.want {
			t.Errorf("%s (%d): outcome = %q; want %s", tc.name, i, outcome, tc.want)
		}
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}
	for i, row := range e.deliveryRows() {
		if row.target != nil {
			t.Errorf("row %d target = %v; want none, the repository is not the workspace's", i, *row.target)
		}
	}
}

// A delivery for another repository that lacks what its kind needs is malformed, not mismatched.
func TestIngressForeignDeliveryWithoutItsIdentityIsMalformed(t *testing.T) {
	e, _ := newIngressEnv(t)
	body := strings.ReplaceAll(githubPayload("labeled", map[string]any{"label": map[string]any{"name": "x"}, "issue": map[string]any{"title": "t"}}), "acme/widgets", "other/repo")
	if rec := e.deliver("widgets", githubDelivery(ingressSecret, "d-1", "issues", body)); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", rec.Code)
	}
}

// names returns n names with the prefix.
func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%03d", prefix, i)
	}
	return out
}

// A GitLab update that changes many labels relays the first 100 changes, and an event lists at
// most 100 labels: the events of an update would otherwise grow with the square of its labels.
// Requirement: 32-REQ-7.2
func TestGitLabLabelChangesAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name             string
		removed, added   int
		wantUnl, wantLab int
	}{
		{"exactly 100 changes", 60, 40, 60, 40},
		{"101 changes", 60, 41, 60, 40},
		{"many removed and added", 150, 150, 100, 0},
		{"few removed, many added", 3, 500, 3, 97},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			e, em := newIngressEnv(t)
			previous, current := names("p", tc.removed), names("n", tc.added)
			body := gitlabUpdateHook(map[string]any{"labels": labelChange(previous, current)}, current...)
			if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, "d-1", "Issue Hook", body))); outcome != "relayed" {
				t.Fatalf("outcome = %q; want relayed", outcome)
			}
			unl, lab := 0, 0
			for _, ev := range em.recorded() {
				switch ev.EventType {
				case audit.EventForgeIssueUnlabeled:
					unl++
				case audit.EventForgeIssueLabeled:
					lab++
				}
				if labels, _ := ev.Metadata["labels"].([]string); len(labels) > 100 {
					t.Fatalf("an event lists %d labels; want at most 100", len(labels))
				}
			}
			if unl != tc.wantUnl || lab != tc.wantLab {
				t.Errorf("%d unlabeled and %d labeled events; want %d and %d", unl, lab, tc.wantUnl, tc.wantLab)
			}
			rows := e.deliveryRows()
			if len(rows) != 1 || strings.Count(rows[0].relayed, "hub.forge") != tc.wantUnl+tc.wantLab {
				t.Errorf("rows = %+v; want the row to list the events relayed", rows)
			}
			cut := tc.removed+tc.added > 100
			if got := strings.Contains(logs.String(), "changed more labels"); got != cut {
				t.Errorf("the cut was logged: %v; want %v\n%s", got, cut, logs)
			}
		})
	}
}

// An issue with more labels than GitHub allows has the first 100 in an event.
func TestGitHubLabelListIsBounded(t *testing.T) {
	e, em := newIngressEnv(t)
	var labels []any
	for _, name := range names("l", 150) {
		labels = append(labels, map[string]any{"name": name})
	}
	body := githubPayload("opened", map[string]any{"issue": map[string]any{"number": 7, "title": "t", "labels": labels}})
	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(ingressSecret, "d-1", "issues", body))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events; want 1", len(events))
	}
	if got, _ := events[0].Metadata["labels"].([]string); !reflect.DeepEqual(got, names("l", 100)) {
		t.Errorf("labels = %d names; want the first 100", len(got))
	}
}

// countingBody is a body of size bytes that counts what is read of it.
type countingBody struct {
	left int64
	read atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > b.left {
		n = b.left
	}
	b.left -= n
	b.read.Add(n)
	return int(n), nil
}

// A body of unknown length is not read beyond the cap: the hub stops at 1 MiB + 1 byte.
// Requirement: 32-REQ-3.6
func TestIngressStopsReadingAtTheCap(t *testing.T) {
	e, _ := newIngressEnv(t)
	body := &countingBody{left: 8 << 20}
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", body)
	req.Header = githubDelivery(ingressSecret, "d-1", "ping", "{}").header
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413", rec.Code)
	}
	if n := body.read.Load(); n > 1<<20+1 {
		t.Errorf("the hub read %d bytes of the body; want at most 1 MiB + 1", n)
	}
}

// A GitLab issue with more labels than GitHub allows has the first 100 in an event, as GitHub's.
func TestGitLabIssueLabelListIsBounded(t *testing.T) {
	e, em := newIngressEnv(t)
	body := gitlabPayload(map[string]any{
		"object_attributes": map[string]any{"iid": 7, "action": "open", "title": "t"},
		"labels":            titles(names("l", 150)...),
	})
	if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, "d-1", "Issue Hook", body))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events; want 1", len(events))
	}
	if got, _ := events[0].Metadata["labels"].([]string); !reflect.DeepEqual(got, names("l", 100)) {
		t.Errorf("labels = %d names; want the first 100", len(got))
	}
}
