package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
)

// writer is what the write routes call on the forge. For most of it, the methods are
// issuex's own; what issuex cannot do the way a write route needs is hub's own request,
// sent by rawForge. A client from Config.NewClient that is itself a writer is used as it
// is, which is how tests put a fake in its place.
type writer interface {
	CreateIssue(ctx context.Context, repo issuex.Repo, req issuex.CreateIssueRequest) (issuex.Issue, error)
	ChangeIssue(ctx context.Context, ref issuex.IssueRef, update issueUpdate) (issuex.Issue, error)
	AddComment(ctx context.Context, ref issuex.IssueRef, body string) (string, error)
	AddLabels(ctx context.Context, ref issuex.IssueRef, labels []string) error
	DropLabel(ctx context.Context, ref issuex.IssueRef, label string) error
	CheckIssue(ctx context.Context, ref issuex.IssueRef) error
	GetLabel(ctx context.Context, repo issuex.Repo, name string) (issuex.Label, error)
	MakeLabel(ctx context.Context, repo issuex.Repo, label issuex.Label) (issuex.Label, error)
	CreatePullRequest(ctx context.Context, repo issuex.Repo, req issuex.CreatePullRequestRequest) (issuex.PullRequest, error)
}

// writerFor is the writer for a client built for a write: issuex's client for what it
// does, and hub's own requests for the rest, sent through the shared HTTP client to the
// API root the client was given.
func (h *handlers) writerFor(fc *forgeClient) writer {
	if w, ok := fc.api.(writer); ok {
		return w
	}
	return forgeWriter{
		Client: fc.api,
		raw:    rawForge{client: h.httpClient, forge: fc.forge, root: fc.root, token: fc.token},
	}
}

// forgeWriter is the writer of a real client. Its methods other than the ones it
// defines are issuex's. (The writer's method is not called UpdateIssue: a type cannot be
// both an issuex client and a writer if the two have a method of one name and two types.)
type forgeWriter struct {
	issuex.Client
	raw rawForge
}

// ChangeIssue changes exactly the fields update sets. issuex's UpdateIssue sends only the
// fields that are not empty, so it cannot clear a body or a set of labels or assignees.
func (f forgeWriter) ChangeIssue(ctx context.Context, ref issuex.IssueRef, update issueUpdate) (issuex.Issue, error) {
	return f.raw.updateIssue(ctx, ref, update)
}

// DropLabel removes a label from an issue and returns the forge's not found when it
// answers one: the label is not on the issue, or the issue is not there. issuex's
// RemoveLabel is that on GitLab, whose removal of an absent label succeeds, and on GitHub
// treats the 404 as a success, which would hide a missing issue; there hub sends it.
func (f forgeWriter) DropLabel(ctx context.Context, ref issuex.IssueRef, label string) error {
	if f.raw.forge == issuex.ForgeTypeGitLab {
		return f.Client.RemoveLabel(ctx, ref, label)
	}
	return f.raw.do(ctx, http.MethodDelete, f.raw.issuePath(ref)+"/labels/"+pathSegment(label), nil, nil)
}

// CheckIssue returns nil when the issue is there, and the forge's error when it is not or
// cannot be told. It is one request. issuex's ReadIssue would also list the issue's
// comments, up to five pages, which a caller that only wants to know that the issue is
// there does not need.
func (f forgeWriter) CheckIssue(ctx context.Context, ref issuex.IssueRef) error {
	return f.raw.do(ctx, http.MethodGet, f.raw.issuePath(ref), nil, nil)
}

// GetLabel looks a repository label up by name. issuex has no lookup of a label.
func (f forgeWriter) GetLabel(ctx context.Context, repo issuex.Repo, name string) (issuex.Label, error) {
	var raw wireLabel
	if err := f.raw.do(ctx, http.MethodGet, f.raw.labelsPath(repo)+"/"+pathSegment(name), nil, &raw); err != nil {
		return issuex.Label{}, err
	}
	return raw.label(), nil
}

// MakeLabel creates a repository label and returns it as the forge holds it, which
// issuex's CreateLabel does not: it returns no label, and a label that exists is a success
// to it, so that its caller cannot tell a label it made from one that was there.
func (f forgeWriter) MakeLabel(ctx context.Context, repo issuex.Repo, label issuex.Label) (issuex.Label, error) {
	color := label.Color
	if f.raw.forge == issuex.ForgeTypeGitLab {
		color = "#" + color
	}
	payload := map[string]any{"name": label.Name, "color": color}
	if label.Description != "" {
		payload["description"] = label.Description
	}
	var raw wireLabel
	if err := f.raw.do(ctx, http.MethodPost, f.raw.labelsPath(repo), payload, &raw); err != nil {
		return issuex.Label{}, err
	}
	return raw.label(), nil
}

// issueUpdate is what PATCH /issues/:n sets. A nil field is left as it is; a field that is
// set but empty, an empty body or an empty list, clears.
type issueUpdate struct {
	Title, Body, State *string
	Labels, Assignees  *[]string
}

// fields returns the names of the fields the update sets, sorted.
func (u issueUpdate) fields() []string {
	var names []string
	for name, set := range map[string]bool{
		"assignees": u.Assignees != nil, "body": u.Body != nil, "labels": u.Labels != nil,
		"state": u.State != nil, "title": u.Title != nil,
	} {
		if set {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// githubPayload is the update as GitHub's issue API takes it.
func (u issueUpdate) githubPayload() map[string]any {
	payload := map[string]any{}
	if u.Title != nil {
		payload["title"] = *u.Title
	}
	if u.Body != nil {
		payload["body"] = *u.Body
	}
	if u.State != nil {
		payload["state"] = *u.State
	}
	if u.Labels != nil {
		payload["labels"] = nonNil(*u.Labels)
	}
	if u.Assignees != nil {
		payload["assignees"] = nonNil(*u.Assignees)
	}
	return payload
}

// gitlabPayload is the update as GitLab's issue API takes it: a description, a state event,
// labels joined by commas, and an empty string to clear them. GitLab takes assignees as user
// ids, not names, and issuex sends none either on create or update; so they are not sent.
func (u issueUpdate) gitlabPayload() map[string]any {
	payload := map[string]any{}
	if u.Title != nil {
		payload["title"] = *u.Title
	}
	if u.Body != nil {
		payload["description"] = *u.Body
	}
	if u.State != nil {
		payload["state_event"] = map[string]string{"open": "reopen", "closed": "close"}[*u.State]
	}
	if u.Labels != nil {
		payload["labels"] = strings.Join(*u.Labels, ",")
	}
	return payload
}

// rawForge sends the requests of a write that issuex cannot make, to the forge's API
// through the shared HTTP client, so that the user agent, the redirect policy, the token
// rules and the deadline of the call are the same as for issuex's.
//
// issuex retries a rate-limited request once; these requests are not retried at all.
type rawForge struct {
	client *http.Client
	forge  issuex.ForgeType
	// root is the API root without a trailing slash, with /api/v4 for GitLab.
	root  string
	token string
}

// issuePath is the path of an issue in the forge's API.
func (r rawForge) issuePath(ref issuex.IssueRef) string {
	if r.forge == issuex.ForgeTypeGitLab {
		return fmt.Sprintf("/projects/%s/issues/%d", gitlabProject(ref.Repo), ref.Number)
	}
	return fmt.Sprintf("/repos/%s/%s/issues/%d", pathSegment(ref.Repo.Owner), pathSegment(ref.Repo.Name), ref.Number)
}

// pathSegment is s as one segment of a request path. url.PathEscape leaves "." and ".." as
// they are, and a dot segment is resolved by a forge or a proxy: a label named "." would
// remove a path segment and, with it, every label of the issue. They are sent as %2E.
func pathSegment(s string) string {
	switch s {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	}
	return url.PathEscape(s)
}

// updateIssue is PATCH on GitHub's issue and PUT on GitLab's.
func (r rawForge) updateIssue(ctx context.Context, ref issuex.IssueRef, update issueUpdate) (issuex.Issue, error) {
	if r.forge == issuex.ForgeTypeGitLab {
		var raw gitlabWireIssue
		if err := r.do(ctx, http.MethodPut, r.issuePath(ref), update.gitlabPayload(), &raw); err != nil {
			return issuex.Issue{}, err
		}
		return raw.issue(), nil
	}
	var raw githubWireIssue
	if err := r.do(ctx, http.MethodPatch, r.issuePath(ref), update.githubPayload(), &raw); err != nil {
		return issuex.Issue{}, err
	}
	return raw.issue(), nil
}

// labelsPath is the path of a repository's labels in the forge's API.
func (r rawForge) labelsPath(repo issuex.Repo) string {
	if r.forge == issuex.ForgeTypeGitLab {
		return "/projects/" + gitlabProject(repo) + "/labels"
	}
	return fmt.Sprintf("/repos/%s/%s/labels", pathSegment(repo.Owner), pathSegment(repo.Name))
}

// wireLabel is a label as GitHub and GitLab answer it; a missing description is null.
type wireLabel struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

func (l wireLabel) label() issuex.Label {
	return issuex.Label{Name: l.Name, Color: l.Color, Description: l.Description}
}

// gitlabProject is a GitLab project's path as one URL segment.
func gitlabProject(repo issuex.Repo) string {
	return url.PathEscape(repo.String())
}

// do sends one request with payload as its JSON body and decodes a 2xx answer into out. A
// failure comes back as the *issuex.HTTPError issuex would return for it, which
// mapForgeError maps like any other; an error of the request itself is the HTTP client's.
func (r rawForge) do(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("%s %s: encoding the request: %w", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.root+path, body)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.forge == issuex.ForgeTypeGitLab {
		req.Header.Set("Accept", "application/json")
		if r.token != "" {
			req.Header.Set("PRIVATE-TOKEN", r.token)
		}
	} else {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	data, err := issuex.ReadResponse(resp)
	if err != nil {
		return fmt.Errorf("%s %s: reading the response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return forgeStatusError(method, req.URL.Path, resp, data)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: decoding the response: %w", method, path, err)
		}
	}
	return nil
}

// forgeStatusError is the error issuex returns for a response with a status that is not a
// success: the status, the forge's message and the headers that tell how long to wait.
func forgeStatusError(method, path string, resp *http.Response, data []byte) *issuex.HTTPError {
	first := func(names ...string) string {
		for _, name := range names {
			if v := resp.Header.Get(name); v != "" {
				return v
			}
		}
		return ""
	}
	return &issuex.HTTPError{
		Method:             method,
		Path:               path,
		Status:             resp.StatusCode,
		Message:            forgeMessage(data, resp.StatusCode),
		RetryAfterHeader:   first("Retry-After"),
		RateLimitReset:     first("X-RateLimit-Reset", "RateLimit-Reset"),
		RateLimitRemaining: first("X-RateLimit-Remaining", "RateLimit-Remaining"),
	}
}

// forgeMessage is the message of a forge's error body: GitHub's and GitLab's "message",
// which GitLab makes an object for a validation error, or GitLab's "error"; the status text
// when there is none.
func forgeMessage(data []byte, status int) string {
	var payload struct {
		Message json.RawMessage `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil {
		for _, field := range []json.RawMessage{payload.Message, payload.Error} {
			var text string
			if json.Unmarshal(field, &text) == nil && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
			if len(field) > 0 && string(field) != "null" {
				return string(field)
			}
		}
	}
	return http.StatusText(status)
}

// githubWireIssue is a GitHub issue as the API answers it.
type githubWireIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	PullRequest *struct{}  `json:"pull_request"`
}

func (g githubWireIssue) issue() issuex.Issue {
	var labels []string
	for _, l := range g.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	return issuex.Issue{
		Number: g.Number, Title: g.Title, Body: g.Body, State: g.State,
		URL: g.HTMLURL, HTMLURL: g.HTMLURL, Author: issuex.User{Login: g.User.Login}, Labels: labels,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt, ClosedAt: g.ClosedAt, IsPR: g.PullRequest != nil,
	}
}

// gitlabWireIssue is a GitLab issue as the API answers it.
type gitlabWireIssue struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	WebURL      string `json:"web_url"`
	Author      struct {
		Username string `json:"username"`
	} `json:"author"`
	Labels    []string   `json:"labels"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`
}

func (g gitlabWireIssue) issue() issuex.Issue {
	state := g.State
	if state == "opened" {
		state = "open"
	}
	return issuex.Issue{
		Number: g.IID, Title: g.Title, Body: g.Description, State: state,
		URL: g.WebURL, HTMLURL: g.WebURL, Author: issuex.User{Login: g.Author.Username}, Labels: g.Labels,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt, ClosedAt: g.ClosedAt,
	}
}
