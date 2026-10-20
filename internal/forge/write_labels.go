package forge

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// addLabels is POST /workspaces/:slug/issues/:n/labels.
var addLabels = writeRoute{
	operation: "AddLabels",
	event:     eventKind{audit.EventForgeLabelAdd, "label", "add"},
	prepare:   prepareAddLabels,
}

// prepareAddLabels reads a bare JSON array of 1 to 100 label names, none blank, and adds
// them to the issue, whose other labels stay. A name the repository does not have is for
// the forge to deal with, and its answer is passed through.
func prepareAddLabels(c echo.Context, data []byte) (*writePlan, *refusal) {
	n, ref := parseNumber(c.Param("n"))
	if ref != nil {
		return nil, ref
	}
	labels, ref := decodeNames("labels", data)
	if ref != nil {
		return nil, ref
	}
	if len(labels) == 0 {
		return nil, badRequest("labels must have at least one entry")
	}
	return &writePlan{
		resourceID: strconv.Itoa(n), number: n, extra: map[string]any{"labels": labels},
		send: func(w *writeRun) (*writeAnswer, error) {
			err := w.call(func(ctx context.Context) error {
				return w.api.AddLabels(ctx, issuex.IssueRef{Repo: w.fc.repo, Number: n}, labels)
			})
			if err != nil {
				return nil, err
			}
			return &writeAnswer{status: http.StatusNoContent}, nil
		},
	}, nil
}

// removeLabel is DELETE /workspaces/:slug/issues/:n/labels/:name.
var removeLabel = writeRoute{
	operation: "RemoveLabel",
	event:     eventKind{audit.EventForgeLabelRemove, "label", "remove"},
	prepare:   prepareRemoveLabel,
}

// lastSegment is the last segment of the request's path, percent-decoded once. It reads
// the path as the client sent it: echo's own path parameter is still encoded when the path
// holds a %2F and decoded when it does not, so decoding that value would sometimes be a
// second decoding, of a name that holds a %.
func lastSegment(r *http.Request) (string, error) {
	path := r.URL.EscapedPath()
	return url.PathUnescape(path[strings.LastIndexByte(path, '/')+1:])
}

// prepareRemoveLabel removes the label named by the last path segment from the issue. It
// answers 204 when the issue no longer has the label, also when it never had it: GitHub
// answers not found to the removal of a label the issue lacks and GitLab does not, so
// hub reads the issue once after a not found, and the answer is 204 when the issue is
// there and the not found of the forge when it is not.
func prepareRemoveLabel(c echo.Context, _ []byte) (*writePlan, *refusal) {
	n, ref := parseNumber(c.Param("n"))
	if ref != nil {
		return nil, ref
	}
	label, err := lastSegment(c.Request())
	if err != nil || strings.TrimSpace(label) == "" {
		return nil, badRequest("the label name must be a name that is not blank")
	}
	return &writePlan{
		resourceID: strconv.Itoa(n), number: n, extra: map[string]any{"label": label},
		send: func(w *writeRun) (*writeAnswer, error) {
			issue := issuex.IssueRef{Repo: w.fc.repo, Number: n}
			err := w.call(func(ctx context.Context) error { return w.api.DropLabel(ctx, issue, label) })
			if err != nil && issuex.IsNotFound(err) {
				err = w.call(func(ctx context.Context) error { return w.api.CheckIssue(ctx, issue) })
			}
			if err != nil {
				return nil, err
			}
			return &writeAnswer{status: http.StatusNoContent}, nil
		},
	}, nil
}
