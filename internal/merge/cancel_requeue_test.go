package merge

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Cancel ends a queued job and answers {"status":"cancelled"}; a job in any
// other state is 409; a job of another workspace, another type or an
// unknown id is 404; a member is forbidden and a PAT needs merges:write.
func TestCancel_QueuedJobsOnly(t *testing.T) {
	v := newEnv(t)
	for id, status := range map[string]string{"queued": "queued", "running": "running", "completed": "completed", "cancelled": "cancelled", "dead": "dead_letter"} {
		v.seedJob(t, id, status, ws, "main", "feature/"+id, "", "")
	}
	v.seedJob(t, "other", "queued", "other-ws", "main", "feature/a", "", "")
	v.seedJob(t, "sync", "queued", ws, "main", "feature/sync", "", "")
	testutil.Exec(t, v.db, `UPDATE jobs SET type = 'sync' WHERE id = 'sync'`)
	rec := v.do(t, http.MethodDelete, merges+"/queued", "", testutil.User(owner))
	if rec.Code != http.StatusOK || testutil.Object(t, rec)["status"] != "cancelled" || v.jobStatus(t, "queued") != "cancelled" {
		t.Errorf("cancel: %d %s, job now %s", rec.Code, rec.Body, v.jobStatus(t, "queued"))
	}
	for _, c := range []struct {
		id     string
		auth   *apikit.AuthInfo
		status int
		msg    string
		typ    string
	}{
		{"running", testutil.Admin(), http.StatusConflict, "job cannot be cancelled in its current state", ""},
		{"completed", testutil.User(owner), http.StatusConflict, "job cannot be cancelled in its current state", ""},
		{"cancelled", testutil.User(owner), http.StatusConflict, "job cannot be cancelled in its current state", ""},
		{"dead", testutil.User(owner), http.StatusConflict, "job cannot be cancelled in its current state", ""},
		{"other", testutil.User(owner), http.StatusNotFound, "merge job not found", ""},
		{"sync", testutil.User(owner), http.StatusNotFound, "merge job not found", ""},
		{"ghost", testutil.User(owner), http.StatusNotFound, "merge job not found", ""},
		{"queued", testutil.User(member), http.StatusForbidden, "org members have read-only access to this workspace", wsaccess.ErrorTypeForbidden},
		{"queued", testutil.PAT(owner, "merges:read"), http.StatusForbidden, "PAT requires merges:write scope", ""},
		{"queued", testutil.User(stranger), http.StatusNotFound, "workspace not found", ""},
	} {
		rec := v.do(t, http.MethodDelete, merges+"/"+c.id, "", c.auth)
		if rec.Code != c.status || testutil.Error(t, rec).Message != c.msg || testutil.Error(t, rec).ErrorType != c.typ {
			t.Errorf("cancel %s as %+v: %d %s; want %d %q", c.id, c.auth, rec.Code, rec.Body, c.status, c.msg)
		}
	}
}

// Requeue puts a dead-lettered job back in the queue with its retries reset
// and answers the record; a job that is not dead-lettered, or whose key has
// an active job, is 409; the other refusals are those of cancel.
func TestRequeue_DeadLetteredJobsOnly(t *testing.T) {
	v := newEnv(t)
	v.seedJob(t, "dead", "dead_letter", ws, "main", "feature/a", "", `{"reason":"x"}`)
	v.seedJob(t, "blocked", "dead_letter", ws, "main", "feature/b", "", "")
	v.seedJob(t, "active", "queued", ws, "main", "feature/b", "", "")
	v.seedJob(t, "failed", "failed", ws, "main", "feature/c", "", "")
	v.seedJob(t, "other", "dead_letter", "other-ws", "main", "feature/a", "", "")
	testutil.Exec(t, v.db, `UPDATE jobs SET retry_count = 3 WHERE id = 'dead'`)
	rec := v.do(t, http.MethodPost, merges+"/dead/requeue", "", testutil.User(owner))
	if got := testutil.Object(t, rec); rec.Code != http.StatusOK || got["id"] != "dead" || got["status"] != "queued" || got["retry_count"] != float64(0) || v.jobStatus(t, "dead") != "queued" {
		t.Errorf("requeue: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		id     string
		auth   *apikit.AuthInfo
		status int
	}{
		{"blocked", testutil.User(owner), http.StatusConflict},
		{"failed", testutil.User(owner), http.StatusConflict},
		{"other", testutil.User(owner), http.StatusNotFound},
		{"ghost", testutil.User(owner), http.StatusNotFound},
		{"failed", testutil.User(member), http.StatusForbidden},
		{"failed", testutil.PAT(owner, "merges:read"), http.StatusForbidden},
		{"failed", testutil.User(stranger), http.StatusNotFound},
		{"failed", nil, http.StatusUnauthorized},
	} {
		if rec := v.do(t, http.MethodPost, merges+"/"+c.id+"/requeue", "", c.auth); rec.Code != c.status {
			t.Errorf("requeue %s as %+v: %d %s; want %d", c.id, c.auth, rec.Code, rec.Body, c.status)
		}
	}
	if v.jobStatus(t, "blocked") != "dead_letter" || v.jobStatus(t, "failed") != "failed" {
		t.Error("a refused requeue changed a job")
	}
}
