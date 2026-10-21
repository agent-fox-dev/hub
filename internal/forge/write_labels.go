package forge

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
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

// ensureLabel is POST /workspaces/:slug/labels.
var ensureLabel = writeRoute{
	operation: "EnsureLabel",
	event:     eventKind{audit.EventForgeLabelCreate, "label", "create"},
	prepare:   prepareEnsureLabel,
}

// defaultLabelColor is the color of a label the request gives none for: GitLab needs one to
// create a label, and this is GitHub's default.
const defaultLabelColor = "ededed"

// labelColor is six hex digits, with or without one leading #.
var labelColor = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

// prepareEnsureLabel reads {name, color?, description?} and makes sure the repository has a
// label of that name. The label is looked up first: one that exists is answered 200 as the
// forge holds it, and is never changed, whatever color and description the request has,
// so that making sure of a label twice is harmless. A label that is not there is created and
// answered 201. A create the forge refuses is followed by one more lookup, which finds the
// label another call created in the meantime and answers it 200; if it is still not there,
// the answer is the create's error.
func prepareEnsureLabel(_ echo.Context, data []byte) (*writePlan, *refusal) {
	b, ref := parseBody(data)
	if ref != nil {
		return nil, ref
	}
	name, ref := b.required("name")
	if ref != nil {
		return nil, ref
	}
	color, present, ref := b.text("color")
	if ref != nil {
		return nil, ref
	}
	if !present {
		color = defaultLabelColor
	} else if !labelColor.MatchString(color) {
		return nil, badRequest("color must be six hex digits, with or without a leading #")
	}
	description, _, ref := b.text("description")
	if ref != nil {
		return nil, ref
	}
	label := issuex.Label{Name: name, Color: strings.TrimPrefix(color, "#"), Description: description}

	return &writePlan{
		resourceID: name, extra: map[string]any{"label": name, "created": false},
		send: func(w *writeRun) (*writeAnswer, error) {
			lookup := func() (found issuex.Label, err error) {
				err = w.call(func(ctx context.Context) (err error) {
					found, err = w.api.GetLabel(ctx, w.fc.repo, name)
					return err
				})
				return found, err
			}
			found, err := lookup()
			if err == nil {
				return &writeAnswer{status: http.StatusOK, body: found}, nil
			}
			if !issuex.IsNotFound(err) {
				return nil, err
			}

			var created issuex.Label
			createErr := w.call(func(ctx context.Context) (err error) {
				created, err = w.api.MakeLabel(ctx, w.fc.repo, label)
				return err
			})
			if createErr == nil {
				w.set("created", true)
				return &writeAnswer{status: http.StatusCreated, body: created}, nil
			}
			if found, err := lookup(); err == nil {
				return &writeAnswer{status: http.StatusOK, body: found}, nil
			}
			return nil, createErr
		},
	}, nil
}
