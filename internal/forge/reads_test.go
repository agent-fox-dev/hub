package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// readPaths are the nine read routes under /workspaces/:slug.
var readPaths = []string{"/forge/repository", "/issues", "/issues/1", "/issues/1/comments", "/pulls/1", "/pulls/1/state", "/pulls/1/files", "/pulls/1/checks", "/pulls/1/reviews"}

// parseNumber accepts a positive decimal integer, leading zeros included, and nothing else.
func TestParseNumber(t *testing.T) {
	for value, want := range map[string]int{"1": 1, "42": 42, "007": 7, "2147483647": 2147483647} {
		if got, ref := parseNumber(value); ref != nil || got != want {
			t.Errorf("parseNumber(%q) = %d, %+v; want %d", value, got, ref, want)
		}
	}
	for _, value := range []string{"", "0", "-1", "+1", "1.5", "abc", "007x", " 1", "1e3", "٣", "99999999999999999999999"} {
		if got, ref := parseNumber(value); ref == nil || ref.status != 400 || !strings.HasPrefix(ref.message, "n ") {
			t.Errorf("parseNumber(%q) = %d, %+v; want a 400 naming n", value, got, ref)
		}
	}
}

// parseIssueFilter passes only what the query holds, splits and trims the labels, and
// always passes a limit; a bad value is refused naming the parameter.
func TestParseIssueFilter(t *testing.T) {
	for query, want := range map[string]issuex.IssueFilter{
		"":                                  {Limit: 30},
		"labels=%20a%20,%20,b&labels=c":     {Labels: []string{"a", "b", "c"}, Limit: 30},
		"labels=,&assignee=some%20one":      {Assignee: "some one", Limit: 30},
		"state=closed&sort=created&limit=7": {State: "closed", Sort: "created", Limit: 7},
		"direction=desc&limit=100&x=y":      {Direction: "desc", Limit: 100},
	} {
		q, _ := url.ParseQuery(query)
		if got, ref := parseIssueFilter(q); ref != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseIssueFilter(%q) = %+v, %+v; want %+v", query, got, ref, want)
		}
	}
	for query, param := range map[string]string{
		"state=x": "state", "state=": "state", "state=open&state=open": "state", "sort=x": "sort", "direction=up": "direction",
		"limit=x": "limit", "limit=0": "limit", "limit=101": "limit", "limit=-5": "limit", "limit=1.5": "limit", "limit=30&limit=40": "limit",
	} {
		q, _ := url.ParseQuery(query)
		if _, ref := parseIssueFilter(q); ref == nil || ref.status != 400 || !strings.HasPrefix(ref.message, param+" ") {
			t.Errorf("parseIssueFilter(%q) = %+v; want a 400 naming %s", query, ref, param)
		}
	}
}

// Each read route asks the forge about the thing its path names, through issuex, and
// returns the issuex encoding unchanged: a list as a bare array, the issue with its comments
// and the truncation flag; the list query reaches the forge.
func TestReads_ReturnTheIssuexEncoding(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(githubFixture)
	repo := issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}
	direct, err := issuex.NewGitHub(issuex.Options{BaseURL: e.stub.URL, Token: "origin-token", HTTPClient: e.stub.Client(), Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	ctx, issueRef, prRef := context.Background(), issuex.IssueRef{Repo: repo, Number: 1}, issuex.IssueRef{Repo: repo, Number: 1, IsPullRequest: true}
	must := func(v any, err error) any {
		if err != nil {
			t.Fatalf("direct call: %v", err)
		}
		return v
	}
	list, listErr := direct.ListIssues(ctx, repo, issuex.IssueFilter{Limit: 30})
	comments, commentsErr := direct.ListComments(ctx, issueRef)
	want := map[string]struct {
		body any
		path string
	}{
		"/forge/repository":  {must(direct.GetRepository(ctx, repo)), "/repos/acme/widgets"},
		"/issues":            {must(list.Issues, listErr), "/repos/acme/widgets/issues"},
		"/issues/1":          {must(direct.ReadIssue(ctx, issueRef)), "/repos/acme/widgets/issues/1"},
		"/issues/1/comments": {must(comments.Comments, commentsErr), "/repos/acme/widgets/issues/1/comments"},
		"/pulls/1":           {must(direct.ReadPullRequest(ctx, prRef)), "/repos/acme/widgets/pulls/1"},
		"/pulls/1/state":     {must(direct.GetPRState(ctx, prRef)), "/repos/acme/widgets/pulls/1"},
		"/pulls/1/files":     {must(direct.ReadChangedFiles(ctx, prRef)), "/repos/acme/widgets/pulls/1/files"},
		"/pulls/1/checks":    {must(direct.GetCIChecks(ctx, prRef)), "/repos/acme/widgets/commits/abc123/check-runs"},
		"/pulls/1/reviews":   {must(direct.GetPRReviews(ctx, prRef)), "/repos/acme/widgets/pulls/1/reviews"},
	}
	for _, path := range readPaths {
		e.stub.forget()
		rec := e.get("/workspaces/widgets"+path, apiKeyAuth())
		wantJSON, _ := json.Marshal(want[path].body)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != string(wantJSON) {
			t.Errorf("%s: %d %s\nwant %s", path, rec.Code, rec.Body, wantJSON)
		}
		var paths []string
		for _, r := range e.stub.received() {
			paths = append(paths, r.Path)
		}
		if !strings.Contains(fmt.Sprint(paths), want[path].path) {
			t.Errorf("%s: the forge saw %v; want %s", path, paths, want[path].path)
		}
	}
	var thread struct {
		Comments  []json.RawMessage `json:"comments"`
		Truncated *bool             `json:"truncated"`
	}
	testutil.Decode(t, e.get("/workspaces/widgets/issues/1", apiKeyAuth()), &thread)
	if len(thread.Comments) != 2 || thread.Truncated == nil || *thread.Truncated {
		t.Errorf("the thread has %d comments, truncated %v; want 2 and false", len(thread.Comments), thread.Truncated)
	}
	e.stub.forget()
	e.get("/workspaces/widgets/issues?labels=af:triage,,bug&assignee=bob&state=open&limit=5", apiKeyAuth())
	if q := e.stub.received()[0].Query; !strings.Contains(q, "af%3Atriage") || !strings.Contains(q, "bug") || !strings.Contains(q, "state=open") || !strings.Contains(q, "assignee=bob") {
		t.Errorf("the forge saw the query %q; want the labels, the state and the assignee", q)
	}
}

// Empty lists are encoded as [] and never null, the comments of an issue included.
func TestReads_EmptyListsAreArrays(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1"):
			respond(http.StatusOK, pullBody(1, "acme/widgets", "feature", "main", false))(w, r)
		case strings.HasSuffix(r.URL.Path, "/issues/1"):
			respond(http.StatusOK, issueBody(1, "t", ""))(w, r)
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			respond(http.StatusOK, `{"total_count":0,"check_runs":[]}`)(w, r)
		default:
			respond(http.StatusOK, "[]")(w, r)
		}
	})
	for _, path := range []string{"/issues", "/issues/1/comments", "/pulls/1/files", "/pulls/1/checks", "/pulls/1/reviews"} {
		if rec := e.get("/workspaces/widgets"+path, apiKeyAuth()); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Errorf("%s: %d %s; want 200 []", path, rec.Code, rec.Body)
		}
	}
	if rec := e.get("/workspaces/widgets/issues/1", apiKeyAuth()); !strings.Contains(rec.Body.String(), `"comments":[]`) {
		t.Errorf("an issue without comments: %s; want \"comments\":[]", rec.Body)
	}
}

// A bad number or list parameter is 400 naming it, before a credential or variable is read.
func TestReads_BadParametersAreRefusedFirst(t *testing.T) {
	e := widgetsEnv(t)
	for _, path := range []string{"/issues/%s", "/issues/%s/comments", "/pulls/%s", "/pulls/%s/state", "/pulls/%s/files", "/pulls/%s/checks", "/pulls/%s/reviews"} {
		for _, n := range []string{"0", "abc", "1.5", "%201"} {
			rec := e.get("/workspaces/widgets"+fmt.Sprintf(path, n), apiKeyAuth())
			if rec.Code != http.StatusBadRequest || !strings.HasPrefix(testutil.Error(t, rec).Message, "n ") {
				t.Errorf("%s: %d %s; want 400 naming n", fmt.Sprintf(path, n), rec.Code, rec.Body)
			}
		}
	}
	for query, param := range map[string]string{"state=bogus": "state", "limit=101": "limit", "sort=name": "sort"} {
		rec := e.get("/workspaces/widgets/issues?"+query, apiKeyAuth())
		if rec.Code != http.StatusBadRequest || !strings.HasPrefix(testutil.Error(t, rec).Message, param+" ") {
			t.Errorf("?%s: %d %s; want 400 naming %s", query, rec.Code, rec.Body, param)
		}
	}
	if n := e.originCalls.Load() + e.varCalls.Load() + int64(e.stub.count()); n != 0 {
		t.Errorf("%d lookups or requests; want none", n)
	}
}

// Reads need neither the workspace lock nor a clone: an archived and a locked workspace
// answer like a ready one. They record no audit event, for a success or a failure.
func TestReads_NeedNoLockOrCloneAndRecordNothing(t *testing.T) {
	spy := &recordingEmitter{}
	e := widgetsEnv(t, withEmitter(spy))
	e.stub.setHandler(githubFixture)
	addWorkspace(t, e.db, "archived", archived)
	addWorkspace(t, e.db, "locked")
	for _, slug := range []string{"archived", "locked"} {
		e.setVar(slug, "FORGE_API_URL", e.stub.URL)
	}
	unlock := wslock.Lock("locked")
	defer unlock()
	for _, slug := range []string{"widgets", "archived", "locked"} {
		done := make(chan int, 1)
		go func() { done <- e.get("/workspaces/"+slug+"/forge/repository", apiKeyAuth()).Code }()
		select {
		case code := <-done:
			if code != http.StatusOK {
				t.Errorf("%s: status = %d; want 200", slug, code)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the read waits for the workspace lock", slug)
		}
	}
	for _, path := range readPaths {
		e.get("/workspaces/widgets"+path, apiKeyAuth())
	}
	e.stub.setHandler(answer(500, "boom"))
	e.get("/workspaces/widgets/issues", apiKeyAuth())
	if events := spy.recorded(); len(events) != 0 {
		t.Errorf("the reads emitted %d events: %+v", len(events), events)
	}
}
