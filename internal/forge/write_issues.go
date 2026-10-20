package forge

import (
	"context"
	"net/http"
	"strconv"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// createIssue is POST /workspaces/:slug/issues.
var createIssue = writeRoute{
	operation: "CreateIssue",
	event:     eventKind{audit.EventForgeIssueCreate, "issue", "create"},
	prepare:   prepareCreateIssue,
}

// prepareCreateIssue reads {title, body?, labels?, assignees?}: the title must be set and
// not blank, the labels and the assignees at most a hundred names each, none blank.
// Other fields are ignored. The issue is created with the fields as given.
func prepareCreateIssue(_ echo.Context, data []byte) (*writePlan, *refusal) {
	b, ref := parseBody(data)
	if ref != nil {
		return nil, ref
	}
	var req issuex.CreateIssueRequest
	if req.Title, ref = b.required("title"); ref != nil {
		return nil, ref
	}
	if req.Body, _, ref = b.text("body"); ref != nil {
		return nil, ref
	}
	if req.Labels, _, ref = b.names("labels"); ref != nil {
		return nil, ref
	}
	if req.Assignees, _, ref = b.names("assignees"); ref != nil {
		return nil, ref
	}
	return &writePlan{
		texts: []textField{{"body", req.Body}},
		send: func(w *writeRun) (*writeAnswer, error) {
			var issue issuex.Issue
			err := w.call(func(ctx context.Context) (err error) {
				issue, err = w.fc.api.CreateIssue(ctx, w.fc.repo, req)
				return err
			})
			if err != nil {
				return nil, err
			}
			w.resourceID, w.number, w.url = strconv.Itoa(issue.Number), issue.Number, issueURL(issue)
			return &writeAnswer{status: http.StatusCreated, body: issue}, nil
		},
	}, nil
}

// issueURL is the web URL of an issue.
func issueURL(issue issuex.Issue) string {
	if issue.HTMLURL != "" {
		return issue.HTMLURL
	}
	return issue.URL
}
