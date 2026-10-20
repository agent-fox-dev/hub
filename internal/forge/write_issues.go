package forge

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

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
				issue, err = w.api.CreateIssue(ctx, w.fc.repo, req)
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

// updateIssue is PATCH /workspaces/:slug/issues/:n.
var updateIssue = writeRoute{
	operation: "UpdateIssue",
	event:     eventKind{audit.EventForgeIssueUpdate, "issue", "update"},
	prepare:   prepareUpdateIssue,
}

// prepareUpdateIssue reads {title?, body?, state?, labels?, assignees?} and changes only
// the fields the request sets: a field that is absent or null is left alone, a body of ""
// clears the body, and a list replaces the whole set, [] removing every entry. The title
// must not be blank, the state must be open or closed, and a body that sets none of the
// fields is refused.
func prepareUpdateIssue(c echo.Context, data []byte) (*writePlan, *refusal) {
	n, ref := parseNumber(c.Param("n"))
	if ref != nil {
		return nil, ref
	}
	b, ref := parseBody(data)
	if ref != nil {
		return nil, ref
	}
	var update issueUpdate
	if value, present, ref := b.text("title"); ref != nil {
		return nil, ref
	} else if present {
		if strings.TrimSpace(value) == "" {
			return nil, badRequest("title must not be blank")
		}
		update.Title = &value
	}
	if value, present, ref := b.text("body"); ref != nil {
		return nil, ref
	} else if present {
		update.Body = &value
	}
	if value, present, ref := b.text("state"); ref != nil {
		return nil, ref
	} else if present {
		if value != "open" && value != "closed" {
			return nil, badRequest("state must be open or closed")
		}
		update.State = &value
	}
	if list, present, ref := b.names("labels"); ref != nil {
		return nil, ref
	} else if present {
		update.Labels = &list
	}
	if list, present, ref := b.names("assignees"); ref != nil {
		return nil, ref
	} else if present {
		update.Assignees = &list
	}
	fields := update.fields()
	if len(fields) == 0 {
		return nil, badRequest("nothing to update")
	}

	plan := &writePlan{resourceID: strconv.Itoa(n), number: n, extra: map[string]any{"fields": fields}}
	if update.Body != nil {
		plan.texts = []textField{{"body", *update.Body}}
	}
	plan.refuse = func(forge issuex.ForgeType) *refusal {
		if forge != issuex.ForgeTypeGitLab {
			return nil
		}
		// GitLab takes assignees as user ids and the hub sends none, so they change nothing:
		// an update of nothing else has nothing to update, and the event does not claim them.
		changed := slices.DeleteFunc(slices.Clone(fields), func(name string) bool { return name == "assignees" })
		if len(changed) == 0 {
			return badRequest("nothing to update: GitLab takes assignees as user ids, which the hub does not send")
		}
		plan.extra["fields"] = changed
		return nil
	}
	plan.send = func(w *writeRun) (*writeAnswer, error) {
		issue, err := w.changeIssue(n, update)
		if err != nil {
			return nil, err
		}
		return &writeAnswer{status: http.StatusOK, body: issue}, nil
	}
	return plan, nil
}

// changeIssue sends one update of issue n and notes the issue's URL for the event.
func (w *writeRun) changeIssue(n int, update issueUpdate) (issuex.Issue, error) {
	var issue issuex.Issue
	err := w.call(func(ctx context.Context) (err error) {
		issue, err = w.api.ChangeIssue(ctx, issuex.IssueRef{Repo: w.fc.repo, Number: n}, update)
		return err
	})
	if err == nil {
		w.url = issueURL(issue)
	}
	return issue, err
}

// closeIssue is POST /workspaces/:slug/issues/:n/close.
var closeIssue = writeRoute{
	operation: "CloseIssue",
	event:     eventKind{audit.EventForgeIssueClose, "issue", "close"},
	prepare:   prepareCloseIssue,
}

// prepareCloseIssue reads an empty body or {comment?}. A comment that is not blank is
// posted on the issue first, and only when that succeeded is the issue closed, so that a
// failed comment leaves the issue as it was. A close that fails after the comment leaves
// the comment, and the event holds its URL. Closing a closed issue is a close.
func prepareCloseIssue(c echo.Context, data []byte) (*writePlan, *refusal) {
	n, ref := parseNumber(c.Param("n"))
	if ref != nil {
		return nil, ref
	}
	var comment string
	if len(bytes.TrimSpace(data)) > 0 {
		b, ref := parseBody(data)
		if ref != nil {
			return nil, ref
		}
		if comment, _, ref = b.text("comment"); ref != nil {
			return nil, ref
		}
	}
	post := strings.TrimSpace(comment) != ""

	plan := &writePlan{resourceID: strconv.Itoa(n), number: n}
	if post {
		plan.texts = []textField{{"comment", comment}}
	}
	plan.send = func(w *writeRun) (*writeAnswer, error) {
		if post {
			url, err := w.comment(n, false, comment)
			if err != nil {
				return nil, err
			}
			w.set("comment_url", scrub(url, w.fc.token))
		}
		closed := "closed"
		issue, err := w.changeIssue(n, issueUpdate{State: &closed})
		if err != nil {
			return nil, err
		}
		return &writeAnswer{status: http.StatusOK, body: issue}, nil
	}
	return plan, nil
}

// comment posts a comment on issue or pull request n and returns its URL.
func (w *writeRun) comment(n int, pull bool, text string) (string, error) {
	var url string
	err := w.call(func(ctx context.Context) (err error) {
		url, err = w.api.AddComment(ctx, issuex.IssueRef{Repo: w.fc.repo, Number: n, IsPullRequest: pull}, text)
		return err
	})
	return url, err
}

// set adds a field to the metadata of the event.
func (w *writeRun) set(key string, value any) {
	if w.extra == nil {
		w.extra = map[string]any{}
	}
	w.extra[key] = value
}

// commentOnIssue is POST /workspaces/:slug/issues/:n/comments.
var commentOnIssue = writeRoute{
	operation: "AddComment",
	event:     eventKind{audit.EventForgeCommentCreate, "comment", "create"},
	prepare:   prepareComment(false),
}

// prepareComment reads {body}, which must be set and not blank, and posts it as a comment
// on issue or pull request n. The answer is {"url": <the comment's URL>}.
func prepareComment(pull bool) func(echo.Context, []byte) (*writePlan, *refusal) {
	return func(c echo.Context, data []byte) (*writePlan, *refusal) {
		n, ref := parseNumber(c.Param("n"))
		if ref != nil {
			return nil, ref
		}
		b, ref := parseBody(data)
		if ref != nil {
			return nil, ref
		}
		text, ref := b.required("body")
		if ref != nil {
			return nil, ref
		}
		return &writePlan{
			resourceID: strconv.Itoa(n), number: n,
			texts: []textField{{"body", text}},
			send: func(w *writeRun) (*writeAnswer, error) {
				url, err := w.comment(n, pull, text)
				if err != nil {
					return nil, err
				}
				w.url = url
				return &writeAnswer{status: http.StatusCreated, body: map[string]string{"url": url}}, nil
			},
		}, nil
	}
}
