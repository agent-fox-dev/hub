package gitserver

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-34-58 (git server): a bound token refused another repository reports
// exactly once, on the git surface with the git route.
// Requirement: 34-REQ-10.3
func TestGitBinding_ReportsRefusal_TS_34_58(t *testing.T) {
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })

	env := newBindingGitEnv(t)
	seedGitPAT(t, env.db, "boundread", "user-a", "git:read")
	bindGitToken(t, env.db, "boundread", "s")

	if code, _ := env.infoRefs(t, "x", "git-upload-pack", "af_pat_boundread"); code != http.StatusNotFound {
		t.Fatalf("fetch of x: %d; want 404", code)
	}
	want := wsaccess.BoundRefusal{TokenID: "boundread", UserID: "user-a", BoundSlug: "s", RequestedSlug: "x", Method: "GET",
		Route: "/git/:org/:slug.git/info/refs", Status: 404, Surface: wsaccess.SurfaceGit}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("reports %+v; want exactly %+v", reports, want)
	}
	if code, _ := env.infoRefs(t, "s", "git-upload-pack", "af_pat_boundread"); code != http.StatusOK || len(reports) != 1 {
		t.Errorf("fetch of s: %d, reports %d; want 200 and no new report", code, len(reports))
	}
}
