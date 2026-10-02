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
// This hook decouples the git operations from the workspace package, avoiding
// an import cycle while allowing branch validation and local-ref creation at
// patch registration time.
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

// BranchResolveError is the interface that classified resolver errors implement.
// The handler uses errors.As to extract the kind and map it to an HTTP status.
type BranchResolveError interface {
	error
	BranchResolveKind() string
}

// branchResolveError is the concrete implementation of BranchResolveError.
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

func (e *branchResolveError) BranchResolveKind() string {
	return e.kind
}

func (e *branchResolveError) Unwrap() error {
	return e.cause
}

// NewBranchResolveError creates a classified resolver error with the given
// kind and underlying cause. Tests and the carrypatch resolver use this to
// produce errors the handler can map to HTTP status codes.
func NewBranchResolveError(kind string, cause error) error {
	return &branchResolveError{kind: kind, cause: cause}
}

// classifyBranchResolveError extracts the kind from a BranchResolveError.
// Returns the kind string and true if the error carries a classification,
// or ("", false) for unclassified errors.
func classifyBranchResolveError(err error) (string, bool) {
	var bre BranchResolveError
	if errors.As(err, &bre) {
		return bre.BranchResolveKind(), true
	}
	return "", false
}
