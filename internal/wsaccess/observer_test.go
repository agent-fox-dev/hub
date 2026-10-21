package wsaccess_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-34-56: The refusal observer is a no-op when unset, registers and reports
// safely under concurrency, and wsaccess does not import internal/audit.
// Requirement: 34-REQ-10.1
func TestRefusalObserver_TS_34_56(t *testing.T) {
	wsaccess.SetRefusalObserver(nil)
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	wsaccess.ReportBoundRefusal(wsaccess.BoundRefusal{TokenID: "t"}) // no observer: no effect, no panic

	var calls atomic.Int32
	wsaccess.SetRefusalObserver(func(wsaccess.BoundRefusal) { calls.Add(1) })
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wsaccess.ReportBoundRefusal(wsaccess.BoundRefusal{TokenID: "t", Status: 404, Surface: wsaccess.SurfaceAPI})
		}()
	}
	// Registering concurrently with reports is safe too.
	wsaccess.SetRefusalObserver(func(wsaccess.BoundRefusal) { calls.Add(1) })
	wg.Wait()
	if n := calls.Load(); n != 100 {
		t.Errorf("observer calls = %d; want 100", n)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasSuffix(path, "/internal/audit") {
				t.Errorf("%s imports %s; wsaccess must not depend on audit", e.Name(), path)
			}
		}
	}
}

// TS-34-57: A report carries the token id, user id, bound slug, requested
// slug, method, route template, status and surface, as filled by the refusal
// points themselves: the binding guard on the API surface, and
// AuthorizeWorkspace, which has no request (erratum 34 entry 5). The git
// surface is asserted by TestGitBinding_ReportsRefusal_TS_34_58.
// Requirement: 34-REQ-10.2
func TestRefusalObserver_ReportFields_TS_34_57(t *testing.T) {
	var mu sync.Mutex
	var got []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { mu.Lock(); got = append(got, r); mu.Unlock() })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })

	db := openBindingDB(t)
	for _, slug := range []string{"S", "X"} {
		if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id) VALUES (?, 'u')`, slug); err != nil {
			t.Fatal(err)
		}
	}
	bind(t, db, "T", "S")
	e, api := newGuardedAPI(db)
	api.GET("/workspaces/:slug/patches", (&probe{}).handle)
	api.GET("/widgets", (&probe{}).handle)
	pat := &apikit.AuthInfo{CredentialType: "pat", UserID: "u", TokenID: "T"}

	serve(e, "GET", "/api/v1/workspaces/X/patches", pat, "")
	serve(e, "GET", "/api/v1/widgets", pat, "")
	wsaccess.AuthorizeWorkspace(db, pat, "X")

	want := []wsaccess.BoundRefusal{
		{TokenID: "T", UserID: "u", BoundSlug: "S", RequestedSlug: "X", Method: "GET",
			Route: "/api/v1/workspaces/:slug/patches", Status: 404, Surface: wsaccess.SurfaceAPI},
		{TokenID: "T", UserID: "u", BoundSlug: "S", Method: "GET", Route: "/api/v1/widgets", Status: 403, Surface: wsaccess.SurfaceAPI},
		{TokenID: "T", UserID: "u", BoundSlug: "S", RequestedSlug: "X", Status: 404, Surface: wsaccess.SurfaceAPI},
	}
	if len(got) != len(want) {
		t.Fatalf("reports %+v; want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("report %d = %+v; want %+v", i, got[i], want[i])
		}
	}
	if wsaccess.SurfaceAPI != "api" || wsaccess.SurfaceGit != "git" {
		t.Errorf("surfaces = %q, %q; want api, git", wsaccess.SurfaceAPI, wsaccess.SurfaceGit)
	}
}
