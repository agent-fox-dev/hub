package forge

import (
	"context"
	"net/http"
	"strconv"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// openPullRequest is POST /workspaces/:slug/pulls.
var openPullRequest = writeRoute{
	operation: "CreatePullRequest",
	event:     eventKind{audit.EventForgePullRequestCreate, "pull_request", "create"},
	prepare:   prepareOpenPullRequest,
}

// prepareOpenPullRequest reads {title, body?, head, base, draft?} and opens a pull request
// on the target's repository. The title, head and base must be set and not blank; the draft
// must be a boolean and is false when it is not given. The head is sent to the forge as it
// is, so that GitHub's owner:branch opens a pull request from a fork. With ?target=upstream
// on a carry_patch workspace it is opened on the upstream repository, with the upstream
// credential: the target is resolved before this runs, like on every route.
func prepareOpenPullRequest(_ echo.Context, data []byte) (*writePlan, *refusal) {
	b, ref := parseBody(data)
	if ref != nil {
		return nil, ref
	}
	var req issuex.CreatePullRequestRequest
	if req.Title, ref = b.required("title"); ref != nil {
		return nil, ref
	}
	if req.Head, ref = b.required("head"); ref != nil {
		return nil, ref
	}
	if req.Base, ref = b.required("base"); ref != nil {
		return nil, ref
	}
	if req.Body, _, ref = b.text("body"); ref != nil {
		return nil, ref
	}
	if req.Draft, _, ref = b.flag("draft"); ref != nil {
		return nil, ref
	}
	return &writePlan{
		texts: []textField{{"body", req.Body}},
		extra: map[string]any{"head": req.Head, "base": req.Base, "draft": req.Draft},
		send: func(w *writeRun) (*writeAnswer, error) {
			var pr issuex.PullRequest
			err := w.call(func(ctx context.Context) (err error) {
				pr, err = w.api.CreatePullRequest(ctx, w.fc.repo, req)
				return err
			})
			if err != nil {
				return nil, err
			}
			w.resourceID, w.number, w.url = strconv.Itoa(pr.Number), pr.Number, pullRequestURL(pr)
			return &writeAnswer{status: http.StatusCreated, body: pr}, nil
		},
	}, nil
}

// pullRequestURL is the web URL of a pull request.
func pullRequestURL(pr issuex.PullRequest) string {
	if pr.HTMLURL != "" {
		return pr.HTMLURL
	}
	return pr.URL
}

// commentOnPullRequest is POST /workspaces/:slug/pulls/:n/comments. It posts a comment on
// the pull request's conversation, which on both forges is what AddComment does for a
// reference to a pull request, and answers its URL. issuex's PostReviewComment would not
// do: it returns no URL, and on GitHub it creates a review.
var commentOnPullRequest = writeRoute{
	operation: "AddComment",
	event:     eventKind{audit.EventForgePullRequestComment, "pull_request", "comment"},
	prepare:   prepareComment(true),
}
