package forge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"

	"github.com/agent-fox-dev/hub/internal/wslock"
)

// readPaths are the nine read routes under /workspaces/:slug.
var readPaths = []string{
	"/forge/repository", "/issues", "/issues/1", "/issues/1/comments",
	"/pulls/1", "/pulls/1/state", "/pulls/1/files", "/pulls/1/checks", "/pulls/1/reviews",
}

// listPaths are the read routes whose result is a list.
var listPaths = []string{"/issues", "/issues/1/comments", "/pulls/1/files", "/pulls/1/checks", "/pulls/1/reviews"}

// numberPaths are the read routes that take :n; %s is where it goes.
var numberPaths = []string{
	"/issues/%s", "/issues/%s/comments", "/pulls/%s", "/pulls/%s/state",
	"/pulls/%s/files", "/pulls/%s/checks", "/pulls/%s/reviews",
}

// githubEnv is an environment whose workspace "widgets" reads the GitHub fixture.
func githubEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	e := newEnv(t, mods...)
	e.stub.setHandler(githubFixture)
	addWorkspace(t, e.db, "widgets")
	e.setVar("widgets", "FORGE_API_URL", e.stub.URL)
	return e
}

// TS-30-7: A forge:write-only PAT, an api_key and an admin_token pass the scope check.
// Requirement: 30-REQ-2.4
func TestWriteOnlyPATAPIKeyAndAdminPass_TS_30_7(t *testing.T) {
	e := githubEnv(t)
	addWorkspace(t, e.db, "other", func(r *workspaceRow) { r.owner = otherID })
	e.setVar("other", "FORGE_API_URL", e.stub.URL)

	cases := []struct {
		name string
		call func() int
	}{
		{"pat with forge:write only", func() int {
			return e.get("/workspaces/widgets/forge/repository", patAuth("forge:write")).Code
		}},
		{"api key", func() int {
			return e.get("/workspaces/widgets/forge/repository", apiKeyAuth()).Code
		}},
		{"admin token on another user's workspace", func() int {
			return e.get("/workspaces/other/forge/repository", adminAuth()).Code
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := tc.call()
			if code == http.StatusUnauthorized || code == http.StatusForbidden {
				t.Fatalf("status = %d; the scope check refused the credential", code)
			}
			if code != http.StatusOK {
				t.Errorf("status = %d; want 200", code)
			}
		})
	}
}

// TS-30-10: Reads on archived and not-yet-cloned workspaces succeed without the
// workspace lock.
// Requirement: 30-REQ-2.7
func TestReadsDoNotNeedTheWorkspaceLockOrAClone_TS_30_10(t *testing.T) {
	e := newEnv(t)
	e.stub.answerRepository()
	for slug, mod := range map[string]func(*workspaceRow){
		"ready":    func(*workspaceRow) {},
		"archived": func(r *workspaceRow) { r.status = "archived"; r.cloneStatus = "archived" },
		"pending":  func(r *workspaceRow) { r.cloneStatus = "pending" },
		"failed":   func(r *workspaceRow) { r.cloneStatus = "failed" },
		"locked":   func(*workspaceRow) {},
	} {
		addWorkspace(t, e.db, slug, mod)
		e.setVar(slug, "FORGE_API_URL", e.stub.URL)
	}

	// A rebuild is running on "locked", and the lock is held.
	endRebuild, ok := wslock.BeginRebuild("locked")
	if !ok {
		t.Fatal("could not begin a rebuild on the locked workspace")
	}
	defer endRebuild()
	unlock := wslock.Lock("locked")
	defer unlock()

	var first string
	for _, slug := range []string{"ready", "archived", "pending", "failed", "locked"} {
		done := make(chan int, 1)
		var body string
		go func() {
			rec := e.get("/workspaces/"+slug+"/forge/repository", apiKeyAuth())
			body = rec.Body.String()
			done <- rec.Code
		}()
		select {
		case code := <-done:
			if code != http.StatusOK {
				t.Errorf("%s: status = %d; want 200", slug, code)
			}
			if first == "" {
				first = body
			} else if body != first {
				t.Errorf("%s: body %s differs from %s", slug, body, first)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the read did not return; it waits for the workspace lock", slug)
		}
	}
}

// TS-30-32: Each read route calls its issuex method and returns the issuex JSON
// encoding unchanged: a list route returns the list as a bare array.
// Requirement: 30-REQ-7.1
func TestReadRoutesReturnTheIssuexEncoding_TS_30_32(t *testing.T) {
	e := githubEnv(t)
	repo := issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}
	direct, err := issuex.NewGitHub(issuex.Options{BaseURL: e.stub.URL, Token: "origin-token", HTTPClient: e.stub.Client(), Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	issueRef := issuex.IssueRef{Repo: repo, Number: 1}
	prRef := issuex.IssueRef{Repo: repo, Number: 1, IsPullRequest: true}

	must := func(v any, err error) any {
		t.Helper()
		if err != nil {
			t.Fatalf("direct call: %v", err)
		}
		return v
	}
	list, listErr := direct.ListIssues(ctx, repo, issuex.IssueFilter{Limit: 30})
	comments, commentsErr := direct.ListComments(ctx, issueRef)
	repository, repositoryErr := direct.GetRepository(ctx, repo)
	thread, threadErr := direct.ReadIssue(ctx, issueRef)
	pull, pullErr := direct.ReadPullRequest(ctx, prRef)
	state, stateErr := direct.GetPRState(ctx, prRef)
	files, filesErr := direct.ReadChangedFiles(ctx, prRef)
	checks, checksErr := direct.GetCIChecks(ctx, prRef)
	reviews, reviewsErr := direct.GetPRReviews(ctx, prRef)
	want := map[string]any{
		"/forge/repository":  must(repository, repositoryErr),
		"/issues":            must(list.Issues, listErr),
		"/issues/1":          must(thread, threadErr),
		"/issues/1/comments": must(comments.Comments, commentsErr),
		"/pulls/1":           must(pull, pullErr),
		"/pulls/1/state":     must(state, stateErr),
		"/pulls/1/files":     must(files, filesErr),
		"/pulls/1/checks":    must(checks, checksErr),
		"/pulls/1/reviews":   must(reviews, reviewsErr),
	}

	for _, path := range readPaths {
		t.Run(path, func(t *testing.T) {
			rec := e.get("/workspaces/widgets"+path, apiKeyAuth())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
			}
			wantJSON, err := json.Marshal(want[path])
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != string(wantJSON) {
				t.Errorf("body\n%s\nwant\n%s", got, wantJSON)
			}
		})
	}

	// The issue carries its comments and issuex's truncation flag.
	var got struct {
		Comments  []json.RawMessage `json:"comments"`
		Truncated *bool             `json:"truncated"`
	}
	if err := json.Unmarshal(e.get("/workspaces/widgets/issues/1", apiKeyAuth()).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 2 || got.Truncated == nil || *got.Truncated {
		t.Errorf("issue thread: %d comments, truncated %v; want 2 comments and truncated false", len(got.Comments), got.Truncated)
	}

	// Each route asked the forge once or, where issuex follows a link, a few times, and
	// the pull request routes asked about the pull request.
	var sawIssues, sawPull, sawFiles bool
	for _, r := range e.stub.received() {
		sawIssues = sawIssues || strings.HasSuffix(r.Path, "/issues") && strings.Contains(r.Query, "per_page=100")
		sawPull = sawPull || r.Path == "/repos/acme/widgets/pulls/1"
		sawFiles = sawFiles || r.Path == "/repos/acme/widgets/pulls/1/files"
	}
	if !sawIssues || !sawPull || !sawFiles {
		t.Errorf("the stub did not see the requests the routes make: issues %v, pull %v, files %v", sawIssues, sawPull, sawFiles)
	}
}

// TS-30-33: Empty list results are encoded as [] and never null.
// Requirement: 30-REQ-7.2
func TestEmptyListsAreArrays_TS_30_33(t *testing.T) {
	rc := &recordingClient{}
	e := newEnv(t, useClient(rc, nil))
	addWorkspace(t, e.db, "widgets")

	for _, path := range listPaths {
		t.Run(path, func(t *testing.T) {
			rec := e.get("/workspaces/widgets"+path, apiKeyAuth())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
				t.Errorf("body = %s; want []", got)
			}
		})
	}

	// An issue without comments carries [] too.
	rec := e.get("/workspaces/widgets/issues/1", apiKeyAuth())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"comments":[]`) {
		t.Errorf("issue thread without comments: status %d, body %s; want \"comments\":[]", rec.Code, rec.Body)
	}
}

// TS-30-34: A :n that is not a positive decimal integer answers 400 naming the
// parameter, before any client is built.
// Requirement: 30-REQ-7.3
func TestInvalidNumberIsRefused_TS_30_34(t *testing.T) {
	rc := &recordingClient{}
	var built atomic.Int64
	e := newEnv(t, useClient(rc, &built))
	addWorkspace(t, e.db, "widgets")

	for _, route := range numberPaths {
		for _, n := range []string{"0", "-1", "1.5", "abc", "007x", "+1", "1e3", "99999999999999999999999", "%201"} {
			t.Run(route+"/"+n, func(t *testing.T) {
				rec := e.get("/workspaces/widgets"+fmt.Sprintf(route, n), apiKeyAuth())
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d; want 400\n%s", rec.Code, rec.Body)
				}
				if msg := decodeError(t, rec).Message; !strings.HasPrefix(msg, "n ") {
					t.Errorf("message %q does not name the parameter n", msg)
				}
			})
		}
	}
	if built.Load() != 0 || len(rc.called()) != 0 || e.stub.count() != 0 {
		t.Errorf("clients built %d, calls %v, stub requests %d; want none", built.Load(), rc.called(), e.stub.count())
	}
	if n := e.originCalls.Load() + e.varCalls.Load(); n != 0 {
		t.Errorf("%d credential or variable lookups; want none", n)
	}
}

// TS-30-35: Invalid list query parameters answer 400 naming the parameter.
// Requirement: 30-REQ-7.4
func TestInvalidListQueryIsRefused_TS_30_35(t *testing.T) {
	rc := &recordingClient{}
	var built atomic.Int64
	e := newEnv(t, useClient(rc, &built))
	addWorkspace(t, e.db, "widgets")

	for query, param := range map[string]string{
		"state=bogus":          "state",
		"state=":               "state",
		"state=open&state=all": "state",
		"sort=name":            "sort",
		"direction=up":         "direction",
		"limit=0":              "limit",
		"limit=101":            "limit",
		"limit=abc":            "limit",
		"limit=-5":             "limit",
		"limit=1.5":            "limit",
		"limit=":               "limit",
		"limit=30&limit=40":    "limit",
	} {
		t.Run(query, func(t *testing.T) {
			rec := e.get("/workspaces/widgets/issues?"+query, apiKeyAuth())
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d; want 400\n%s", rec.Code, rec.Body)
			}
			if msg := decodeError(t, rec).Message; !strings.HasPrefix(msg, param+" ") {
				t.Errorf("message %q does not name %s", msg, param)
			}
		})
	}
	if built.Load() != 0 || e.stub.count() != 0 {
		t.Errorf("clients built %d, stub requests %d; want none", built.Load(), e.stub.count())
	}
}

// TS-30-36: Valid list query parameters reach issuex with commas split, absent values
// omitted and the limit defaulted to 30.
// Requirement: 30-REQ-7.5
func TestListQueryReachesIssuex_TS_30_36(t *testing.T) {
	cases := []struct {
		query string
		want  issuex.IssueFilter
	}{
		{"labels=af:triage,,bug&assignee=bob&state=open",
			issuex.IssueFilter{State: "open", Labels: []string{"af:triage", "bug"}, Assignee: "bob", Limit: 30}},
		{"", issuex.IssueFilter{Limit: 30}},
		{"state=all&sort=updated&direction=asc&limit=100",
			issuex.IssueFilter{State: "all", Sort: "updated", Direction: "asc", Limit: 100}},
		{"labels=a&labels=b,c&limit=1", issuex.IssueFilter{Labels: []string{"a", "b", "c"}, Limit: 1}},
		{"labels=,&limit=5", issuex.IssueFilter{Limit: 5}},
		{"assignee=some one", issuex.IssueFilter{Assignee: "some one", Limit: 30}},
		{"assignee=Bob-Smith_2", issuex.IssueFilter{Assignee: "Bob-Smith_2", Limit: 30}},
		{"target=origin&unknown=1", issuex.IssueFilter{Limit: 30}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			rc := &recordingClient{}
			e := newEnv(t, useClient(rc, nil))
			addWorkspace(t, e.db, "widgets")

			path := "/workspaces/widgets/issues"
			if tc.query != "" {
				path += "?" + strings.ReplaceAll(tc.query, " ", "%20")
			}
			rec := e.get(path, apiKeyAuth())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
			}
			got := rc.filter
			if got.State != tc.want.State || got.Assignee != tc.want.Assignee || got.Sort != tc.want.Sort ||
				got.Direction != tc.want.Direction || got.Limit != tc.want.Limit || strings.Join(got.Labels, "|") != strings.Join(tc.want.Labels, "|") {
				t.Errorf("filter = %+v; want %+v", got, tc.want)
			}
			if rc.repo != (issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}) {
				t.Errorf("repo = %+v; want the workspace's repository", rc.repo)
			}
		})
	}
}

// The number reaches issuex as an integer on the issue or on the pull request, and the
// route calls the method that is named for it.
// Requirement: 30-REQ-7.1
func TestRoutesCallTheirIssuexMethod(t *testing.T) {
	for path, want := range map[string]struct {
		call string
		pr   bool
	}{
		"/forge/repository":  {"GetRepository", false},
		"/issues":            {"ListIssues", false},
		"/issues/7":          {"ReadIssue", false},
		"/issues/7/comments": {"ListComments", false},
		"/pulls/7":           {"ReadPullRequest", true},
		"/pulls/7/state":     {"GetPRState", true},
		"/pulls/7/files":     {"ReadChangedFiles", true},
		"/pulls/7/checks":    {"GetCIChecks", true},
		"/pulls/7/reviews":   {"GetPRReviews", true},
	} {
		t.Run(path, func(t *testing.T) {
			rc := &recordingClient{}
			e := newEnv(t, useClient(rc, nil))
			addWorkspace(t, e.db, "widgets")
			if rec := e.get("/workspaces/widgets"+path, apiKeyAuth()); rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
			}
			if got := rc.called(); len(got) != 1 || got[0] != want.call {
				t.Fatalf("calls = %v; want [%s]", got, want.call)
			}
			if strings.Contains(path, "7") {
				if rc.ref.Number != 7 || rc.ref.IsPullRequest != want.pr || rc.ref.Repo.Name != "widgets" {
					t.Errorf("ref = %+v; want number 7, pull request %v on acme/widgets", rc.ref, want.pr)
				}
			}
		})
	}
}

// TS-30-37: Forge reads record no audit event: a spy emitter wired into the routes is not
// called, and the database is left as the reads found it.
//
// Spec 30 had the package without an emitter and checked that it did not import
// internal/audit. Spec 31 gives forge.Config an emitter for the writes, so the check is
// the one the spec describes: an emitter spy that no read reaches.
// Requirement: 30-REQ-7.6
func TestReadsRecordNoAuditEvent_TS_30_37(t *testing.T) {
	spy := &recordingEmitter{}
	e := githubEnv(t, withEmitter(spy))
	snapshot := func() string {
		var out []string
		rows, err := e.db.Query(`SELECT type, name, sql FROM sqlite_master ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var a, b string
			var c sql.NullString
			if err := rows.Scan(&a, &b, &c); err != nil {
				t.Fatal(err)
			}
			out = append(out, a+b+c.String)
		}
		var n int
		if err := e.db.QueryRow(`SELECT count(*) FROM workspaces`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return strings.Join(out, ";") + fmt.Sprint(n)
	}
	before := snapshot()
	for _, path := range readPaths {
		e.get("/workspaces/widgets"+path, apiKeyAuth())
	}
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) })
	for _, path := range readPaths {
		e.get("/workspaces/widgets"+path, apiKeyAuth())
	}
	if after := snapshot(); after != before {
		t.Errorf("the database changed during the reads:\n%s\n%s", before, after)
	}
	if events := spy.recorded(); len(events) != 0 {
		t.Errorf("the reads emitted %d events: %+v", len(events), events)
	}
}
