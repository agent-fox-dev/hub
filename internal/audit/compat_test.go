package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// ---------------------------------------------------------------------------
// Spec 36 — compatibility and the single rule
// Requirements: 36-REQ-10.1, 36-REQ-10.2, 36-REQ-10.3
// ---------------------------------------------------------------------------

// compatRequest is a request on a route of this spec for workspace w with
// session sess-w.
type compatRequest struct {
	name, method, path, body string
}

// compatRequests are the requests the compatibility properties send.
func compatRequests() []compatRequest {
	run := "/api/v1/workspaces/w/runs/" + testRunID
	return []compatRequest{
		{"run events", http.MethodGet, run + "/events", ""},
		{"transcript", http.MethodGet, run + "/transcript?node_id=n", ""},
		{"audit filter", http.MethodGet, "/api/v1/audit?workspace=w", ""},
		{"session", http.MethodGet, "/api/v1/sessions/sess-w", ""},
		{"session usage", http.MethodGet, "/api/v1/sessions/sess-w/usage", ""},
		{"session list", http.MethodGet, "/api/v1/sessions?workspace_slug=w", ""},
		{"cost", http.MethodGet, "/api/v1/workspaces/w/cost", ""},
		{"open session", http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"w"}`},
		{"ingest", http.MethodPost, run + "/events", `{"event_type":"session.start","payload":{}}`},
	}
}

// compatAnswer is a status with the error message and type of a refusal.
type compatAnswer struct {
	status           int
	message, errType string
}

// legacyAnswer is what the routes answered before this spec, to the owner,
// a stranger or an admin token, for workspace w of the given status.
func legacyAnswer(r compatRequest, caller, status string) compatAnswer {
	sessionRead := r.name == "session" || r.name == "session usage" || r.name == "session list" || r.name == "cost"
	switch {
	case caller == "admin" && r.method == http.MethodPost:
		return compatAnswer{status: http.StatusCreated}
	case caller == "admin":
		return compatAnswer{status: http.StatusOK}
	case r.name == "ingest" && status == "archived":
		return compatAnswer{http.StatusConflict, "workspace is archived", "workspace_archived"}
	case r.name == "ingest" && caller == "stranger":
		return compatAnswer{http.StatusForbidden, "token owner does not have write access to this workspace", "workspace_access_denied"}
	case sessionRead && (caller == "stranger" || status != "active"):
		return compatAnswer{http.StatusForbidden, "workspace access denied", ""}
	case caller == "stranger":
		return compatAnswer{http.StatusNotFound, "workspace not found", ""}
	case r.method == http.MethodPost:
		return compatAnswer{status: http.StatusCreated}
	}
	return compatAnswer{status: http.StatusOK}
}

// newCompatEnv mounts every route of this spec on a SQLite-backed
// environment with workspace w owned by u1 in org (NULL when org is "") of
// the given status, session sess-w in it, and u3 the owner and only member
// of org X with workspace w-x.
func newCompatEnv(t *testing.T, org, status string, members ...string) *auditTestEnv {
	t.Helper()
	env := newAuditTestEnvWithSQLite(t)
	query := env.echo.Group("/api/v1")
	query.Use(testAuthMiddleware())
	RegisterAuditQueryRoutes(query, env.store, &mockSSEManager{}, env.sqliteDB)
	if org == "" {
		env.seedWorkspaceWithStatus(t, "w", "u1", status)
	} else {
		env.seedWorkspaceInOrg(t, "w", "u1", org, status)
	}
	for _, u := range members {
		seedOrgMember(t, env.sqliteDB, org, u)
	}
	env.seedWorkspaceInOrg(t, "w-x", "u3", "X", "active")
	seedOrgMember(t, env.sqliteDB, "X", "u3")
	env.seedSession(t, &Session{ID: "sess-w", WorkspaceSlug: "w", Status: "active", CredentialID: "u1", CredentialType: "pat"})
	return env
}

// answerOf calls r as auth and returns its status and refusal.
func answerOf(t *testing.T, env *auditTestEnv, r compatRequest, auth *apikit.AuthInfo) compatAnswer {
	t.Helper()
	rec := env.doJSON(t, r.method, r.path, r.body, auth)
	a := compatAnswer{status: rec.Code}
	if rec.Code >= 400 {
		e := errOf(t, rec)
		a.message, a.errType = e.Error.Message, e.Error.ErrorType
	}
	return a
}

// allScopes is a PAT of user with every scope the routes of this spec need.
func allScopes(user string) *apikit.AuthInfo {
	return patAuth(user, "audit:read", "audit:write", "sessions:read", "sessions:write")
}

// TS-36-43 (property): for any stranger to w, whatever w's org and status,
// every route of this spec answers as before. Requirement: 36-REQ-10.1
func TestCompat_PropertyStrangerUnchanged_TS_36_43(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		rng := rand.New(rand.NewSource(seed))
		org := []string{"", "O", "P"}[rng.Intn(3)]
		status := []string{"active", "archived"}[rng.Intn(2)]
		var members []string
		if org != "" {
			members = []string{"u1"}
			if rng.Intn(2) == 0 {
				members = append(members, "u2")
			}
		}
		env := newCompatEnv(t, org, status, members...)
		for _, r := range compatRequests() {
			if got, want := answerOf(t, env, r, allScopes("u3")), legacyAnswer(r, "stranger", status); got != want {
				t.Errorf("seed %d (org %q, %s, members %v): %s: %+v; want %+v", seed, org, status, members, r.name, got, want)
			}
		}
	}
}

// TS-36-44 (property): a workspace in a personal org whose only member is
// its owner, or with a NULL org_id, answers the owner, a stranger and an
// admin token as before. Requirement: 36-REQ-10.2
func TestCompat_PropertyPersonalAndNullOrg_TS_36_44(t *testing.T) {
	callers := map[string]*apikit.AuthInfo{"owner": allScopes("u1"), "stranger": allScopes("u3"), "admin": adminAuth()}
	for _, org := range []string{"", "personal-u1"} {
		for _, status := range []string{"active", "archived"} {
			for name, auth := range callers {
				var members []string
				if org != "" {
					members = []string{"u1"}
				}
				env := newCompatEnv(t, org, status, members...)
				for _, r := range compatRequests() {
					if got, want := answerOf(t, env, r, auth), legacyAnswer(r, name, status); got != want {
						t.Errorf("org %q, %s, %s: %s: %+v; want %+v", org, status, name, r.name, got, want)
					}
				}
			}
		}
	}
}

// TS-36-45: no read decision in internal/audit compares owner_id with the
// caller: ownedWorkspaces is gone, owner_id is queried only in
// checkWorkspaceAccess, and the four read helpers reach
// wsaccess.AuthorizeWorkspace or wsaccess.ReadableSlugs.
// Requirement: 36-REQ-10.3
func TestSingleRule_SourceScan_TS_36_45(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	funcs := map[string]*ast.FuncDecl{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
				funcs[fn.Name.Name] = fn
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "owner_id") && fn.Name.Name != "checkWorkspaceAccess" {
					t.Errorf("%s: %s queries owner_id: %s", fset.Position(lit.Pos()), fn.Name.Name, lit.Value)
				}
				return true
			})
			return false
		})
	}
	if _, ok := funcs["ownedWorkspaces"]; ok {
		t.Error("ownedWorkspaces is still declared")
	}

	// reaches reports whether fn calls wsaccess.AuthorizeWorkspace or
	// wsaccess.ReadableSlugs, itself or through a function of this package.
	var reaches func(name string, seen map[string]bool) bool
	reaches = func(name string, seen map[string]bool) bool {
		fn, ok := funcs[name]
		if !ok || seen[name] {
			return false
		}
		seen[name] = true
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || found {
				return !found
			}
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := f.X.(*ast.Ident); ok && x.Name == "wsaccess" && (f.Sel.Name == "AuthorizeWorkspace" || f.Sel.Name == "ReadableSlugs") {
					found = true
				}
			case *ast.Ident:
				found = reaches(f.Name, seen)
			}
			return !found
		})
		return found
	}
	for _, helper := range []string{"requireWorkspaceRead", "hasWorkspaceAccess", "readableWorkspaces", "getAccessibleWorkspaces"} {
		if _, ok := funcs[helper]; !ok {
			t.Errorf("%s is not declared", helper)
			continue
		}
		if !reaches(helper, map[string]bool{}) {
			t.Errorf("%s does not reach wsaccess.AuthorizeWorkspace or wsaccess.ReadableSlugs", helper)
		}
	}
}
