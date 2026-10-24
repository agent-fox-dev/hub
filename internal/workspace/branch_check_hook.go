package workspace

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
// The carrypatch package implements it; main.go registers it with
// RegisterBranchCheckHook.
type BranchResolverFunc func(ctx context.Context, slug, branchName string) (string, error)

// branchCheckHook is the registered branch-resolver handler. When non-nil,
// handleAddPatch calls it to validate and resolve the branch before inserting.
var branchCheckHook BranchResolverFunc

// RegisterBranchCheckHook registers a function to resolve branch existence
// in workspace repositories. Called from main.go during server initialization.
func RegisterBranchCheckHook(fn BranchResolverFunc) {
	branchCheckHook = fn
}

// Branch resolve error kind constants.
const (
	BranchResolveKindNotFound          = "not_found"
	BranchResolveKindOriginFetchFailed = "origin_fetch_failed"
	BranchResolveKindOriginCredentials = "origin_credentials"
	BranchResolveKindWorkspaceBusy     = "workspace_busy"
)

// branchResolveError is a classified resolver error: one of the
// BranchResolveKind* constants and its cause. NewBranchResolveError makes one
// and BranchResolveKindOf reads it back.
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

// NewBranchResolveError creates a classified resolver error with the given
// kind and underlying cause. The carrypatch resolver returns these so that
// the patch handlers can map them to HTTP status codes.
func NewBranchResolveError(kind string, cause error) error {
	return &branchResolveError{kind: kind, cause: cause}
}

// BranchResolveKindOf returns the kind of a classified resolver error and
// true, or ("", false) for an error that carries no classification.
func BranchResolveKindOf(err error) (string, bool) {
	var bre *branchResolveError
	if errors.As(err, &bre) {
		return bre.kind, true
	}
	return "", false
}
