package forge

import (
	"context"
	"errors"
	"net/http"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// readOp is a call to the forge for a built client. Its result is what the route
// answers 200 with.
type readOp func(ctx context.Context, fc *forgeClient) (any, error)

// prepareFunc reads and checks the parameters of a route, after the target and before
// a client is built. It returns the call to make, or the refusal to answer.
type prepareFunc func(c echo.Context) (readOp, *refusal)

// serve is the handler of a read route; operation names the issuex method it calls. It
// applies the checks every forge route shares, then the target, then the route's own
// parameters, and only then builds the client and calls the forge.
func (h *handlers) serve(operation string, prepare prepareFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req, err := h.authorize(c, readAccess)
		if req == nil {
			return err
		}
		tgt, ref := resolveTarget(c.QueryParams(), req.remotes)
		if ref != nil {
			return writeRefusal(c, ref)
		}
		op, ref := prepare(c)
		if ref != nil {
			return writeRefusal(c, ref)
		}
		fc, err := h.buildClient(req, tgt, false)
		if err != nil {
			return writeForgeError(c, err)
		}
		ctx, cancel := context.WithTimeout(withCredential(c.Request().Context(), fc.token), callDeadline)
		defer cancel()
		result, err := op(ctx, fc)
		if err != nil {
			return h.failCall(c, req, fc, operation, err)
		}
		return c.JSON(http.StatusOK, result)
	}
}

// writeRefusal answers a request that hub refuses before it contacts the forge.
func writeRefusal(c echo.Context, ref *refusal) error {
	return apikit.WriteAPIErrorWithType(c, ref.status, ref.message, ref.errorType)
}

// prepareRepository serves GET /workspaces/:slug/forge/repository.
func prepareRepository(echo.Context) (readOp, *refusal) {
	return func(ctx context.Context, fc *forgeClient) (any, error) {
		return fc.api.GetRepository(ctx, fc.repo)
	}, nil
}

// prepareIssues serves GET /workspaces/:slug/issues. The result is the list of issues
// as a bare array.
func prepareIssues(c echo.Context) (readOp, *refusal) {
	filter, ref := parseIssueFilter(c.QueryParams())
	if ref != nil {
		return nil, ref
	}
	return func(ctx context.Context, fc *forgeClient) (any, error) {
		list, err := fc.api.ListIssues(ctx, fc.repo, filter)
		if err != nil {
			return nil, err
		}
		return nonNil(list.Issues), nil
	}, nil
}

// numberedCall is a call to the forge about one issue or pull request.
type numberedCall func(ctx context.Context, fc *forgeClient, ref issuex.IssueRef) (any, error)

// numbered prepares a route on /issues/:n or /pulls/:n: the number is read before a
// client is built, and the call gets a reference to the issue or the pull request.
func numbered(pull bool, call numberedCall) prepareFunc {
	return func(c echo.Context) (readOp, *refusal) {
		n, ref := parseNumber(c.Param("n"))
		if ref != nil {
			return nil, ref
		}
		return func(ctx context.Context, fc *forgeClient) (any, error) {
			return call(ctx, fc, issuex.IssueRef{Repo: fc.repo, Number: n, IsPullRequest: pull})
		}, nil
	}
}

// valueOf adapts an issuex method that returns one value.
func valueOf[T any](method func(issuex.Client, context.Context, issuex.IssueRef) (T, error)) numberedCall {
	return func(ctx context.Context, fc *forgeClient, ref issuex.IssueRef) (any, error) {
		return method(fc.api, ctx, ref)
	}
}

// listOf adapts an issuex method that returns a list, which is answered as a bare
// array, empty as [].
func listOf[T any](method func(issuex.Client, context.Context, issuex.IssueRef) ([]T, error)) numberedCall {
	return func(ctx context.Context, fc *forgeClient, ref issuex.IssueRef) (any, error) {
		items, err := method(fc.api, ctx, ref)
		if err != nil {
			return nil, err
		}
		return nonNil(items), nil
	}
}

// readIssue is GET /workspaces/:slug/issues/:n: the issue with its comments and issuex's
// truncation flag. When only the comments could not be read, issuex returns the issue
// with the error; the answer carries it as issuex has it, but with the token and the
// URL userinfo taken out of its text.
func readIssue(ctx context.Context, fc *forgeClient, ref issuex.IssueRef) (any, error) {
	thread, err := fc.api.ReadIssue(ctx, ref)
	if err != nil {
		return nil, err
	}
	thread.Comments = nonNil(thread.Comments)
	if thread.CommentsErr != nil {
		thread.CommentsErr = scrubbedCommentsErr(thread.CommentsErr, fc.token)
	}
	return thread, nil
}

// scrubbedCommentsErr returns err for the answer. An *issuex.HTTPError keeps its fields
// and its message is scrubbed; any other error, which encodes as {}, is replaced with
// one made of its scrubbed text.
func scrubbedCommentsErr(err error, token string) error {
	if he, ok := err.(*issuex.HTTPError); ok {
		clean := *he
		clean.Message = scrub(he.Message, token)
		return &clean
	}
	return errors.New(scrub(err.Error(), token))
}

// readComments is GET /workspaces/:slug/issues/:n/comments: the comments as a bare array.
func readComments(ctx context.Context, fc *forgeClient, ref issuex.IssueRef) (any, error) {
	list, err := fc.api.ListComments(ctx, ref)
	if err != nil {
		return nil, err
	}
	return nonNil(list.Comments), nil
}

// nonNil returns s, or an empty list for a nil one, so that it encodes as [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
