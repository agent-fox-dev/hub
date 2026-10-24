package carrypatch

import (
	"context"
	"errors"
	"fmt"
)

// Resolution method constants returned by BranchResolverFunc.
const (
	ResolutionLocal          = "local"
	ResolutionOriginTracking = "origin_tracking"
	ResolutionOriginFetch    = "origin_fetch"
	ResolutionSkipped        = "skipped"
)

// BranchResolverFunc resolves a branch name in a workspace trunk and returns
// the resolution method (one of the Resolution* constants) and a classified
// error. When the error is nil the method describes how the branch was found.
//
// NewBranchResolverHook builds the production implementation; the patch
// handlers take it from APIConfig.ResolveBranch.
type BranchResolverFunc func(ctx context.Context, slug, branchName string) (string, error)

// Branch resolve error kind constants.
const (
	BranchResolveKindNotFound          = "not_found"
	BranchResolveKindOriginFetchFailed = "origin_fetch_failed"
	BranchResolveKindOriginCredentials = "origin_credentials"
	BranchResolveKindWorkspaceBusy     = "workspace_busy"
)

// branchResolveError is a classified resolver error: one of the
// BranchResolveKind* constants and its cause. newBranchResolveError makes one
// and branchResolveKindOf reads it back.
type branchResolveError struct {
	kind  string
	cause error
}

func (e *branchResolveError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.kind, e.cause)
	}
	return e.kind
}

func (e *branchResolveError) Unwrap() error {
	return e.cause
}

// newBranchResolveError creates a classified resolver error with the given
// kind and underlying cause, which the patch handlers map to HTTP status codes.
func newBranchResolveError(kind string, cause error) error {
	return &branchResolveError{kind: kind, cause: cause}
}

// branchResolveKindOf returns the kind of a classified resolver error and
// true, or ("", false) for an error that carries no classification.
func branchResolveKindOf(err error) (string, bool) {
	var bre *branchResolveError
	if errors.As(err, &bre) {
		return bre.kind, true
	}
	return "", false
}
