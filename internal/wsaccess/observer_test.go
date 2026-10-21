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
// slug, method, route, status and surface to the observer unchanged, for the
// API and the git surface. (The refusal points that fill it are covered by
// TS-34-58.) Requirement: 34-REQ-10.2
func TestRefusalObserver_ReportFields_TS_34_57(t *testing.T) {
	var mu sync.Mutex
	var got []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { mu.Lock(); got = append(got, r); mu.Unlock() })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })

	api := wsaccess.BoundRefusal{TokenID: "T", UserID: "u", BoundSlug: "S", RequestedSlug: "X", Method: "GET",
		Route: "/api/v1/workspaces/:slug/patches", Status: 404, Surface: wsaccess.SurfaceAPI}
	git := wsaccess.BoundRefusal{TokenID: "T", UserID: "u", BoundSlug: "S", RequestedSlug: "X", Method: "GET",
		Route: "/git/:org/:slug.git/info/refs", Status: 404, Surface: wsaccess.SurfaceGit}
	wsaccess.ReportBoundRefusal(api)
	wsaccess.ReportBoundRefusal(git)
	if len(got) != 2 || got[0] != api || got[1] != git {
		t.Fatalf("observer got %+v; want %+v and %+v", got, api, git)
	}
	if wsaccess.SurfaceAPI != "api" || wsaccess.SurfaceGit != "git" {
		t.Errorf("surfaces = %q, %q; want api, git", wsaccess.SurfaceAPI, wsaccess.SurfaceGit)
	}
}
