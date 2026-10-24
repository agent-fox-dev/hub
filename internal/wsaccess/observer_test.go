package wsaccess

import "testing"

// The registered observer receives every reported refusal; a nil observer
// unregisters it, after which reporting is a no-op.
func TestRefusalObserver(t *testing.T) {
	ReportBoundRefusal(BoundRefusal{TokenID: "unobserved"})
	got := captureRefusals(t)
	r := BoundRefusal{TokenID: "tok", UserID: "u", BoundSlug: "w", RequestedSlug: "x", Method: "GET", Route: "/r", Status: 404, Surface: SurfaceGit}
	ReportBoundRefusal(r)
	SetRefusalObserver(nil)
	ReportBoundRefusal(BoundRefusal{TokenID: "after"})
	if len(*got) != 1 || (*got)[0] != r {
		t.Errorf("observed %+v, want exactly %+v", *got, r)
	}
}
