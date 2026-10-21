package workspace

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-34-58 (lookupWorkspaceForAuth): a binding refusal reached without the
// guard reports exactly once, with the request's method and route.
// Requirement: 34-REQ-10.3
func TestLookupWorkspaceForAuth_ReportsRefusal_TS_34_58(t *testing.T) {
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })

	db := openTestDB(t)
	for _, slug := range []string{"s", "x"} {
		if err := insertWorkspace(db, &Workspace{Slug: slug, GitURL: "https://github.com/org/" + slug, OwnerID: "u1", Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	bindToken(t, db, "tokB", "s")
	bound := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokB"}

	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/x", nil), httptest.NewRecorder())
	c.SetPath("/api/v1/workspaces/:slug")
	if ws, _ := lookupWorkspaceForAuth(c, db, "x", bound); ws != nil {
		t.Fatal("lookupWorkspaceForAuth(x) granted a bound token another workspace")
	}
	want := wsaccess.BoundRefusal{TokenID: "tokB", UserID: "u1", BoundSlug: "s", RequestedSlug: "x", Method: "GET",
		Route: "/api/v1/workspaces/:slug", Status: 404, Surface: wsaccess.SurfaceAPI}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("reports %+v; want exactly %+v", reports, want)
	}
	c = echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/s", nil), httptest.NewRecorder())
	if ws, _ := lookupWorkspaceForAuth(c, db, "s", bound); ws == nil || len(reports) != 1 {
		t.Errorf("its own workspace: %v, reports %d; want it granted without a report", ws, len(reports))
	}
}
