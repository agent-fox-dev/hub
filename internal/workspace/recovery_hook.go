package workspace

import (
	"context"

	"github.com/txsvc/apikit"
)

// ResetAction describes the outcome of a reset-to-origin operation.
type ResetAction string

const (
	ResetActionNone          ResetAction = "none"
	ResetActionCreated       ResetAction = "created"
	ResetActionFastForwarded ResetAction = "fast_forwarded"
	ResetActionReplaced      ResetAction = "replaced"
)

// ResetResult holds the outcome of a reset-to-origin operation.
type ResetResult struct {
	Action           ResetAction
	LocalSHA         string // tip before the reset; empty when the local branch was absent
	OriginSHA        string // fork tip the branch was moved to
	ReplacedSHA      string // backup SHA; non-empty only for action "replaced"
	RebuildTriggered bool
	RebuildJobID     string // non-empty only when a rebuild job was enqueued
}

// ResetErrorKind classifies errors returned by RunReset.
type ResetErrorKind string

const (
	ResetErrBusy              ResetErrorKind = "workspace_busy"
	ResetErrMissingOnOrigin   ResetErrorKind = "missing_on_origin"
	ResetErrFetchFailed       ResetErrorKind = "origin_fetch_failed"
	ResetErrCredentialFailed  ResetErrorKind = "credential_failed"
	ResetErrRefChanged        ResetErrorKind = "ref_changed"
	ResetErrOther             ResetErrorKind = "other"
)

// ResetError is a classified error returned by RunReset.
type ResetError struct {
	Kind    ResetErrorKind
	Message string // human-readable detail
	Cause   error  // underlying error, may be nil
}

func (e *ResetError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *ResetError) Unwrap() error {
	return e.Cause
}

// ResetPatchInfo carries the patch data the reset operation needs.
// It is defined in the workspace package so that carrypatch does not
// need to import workspace.
type ResetPatchInfo struct {
	ID                string
	BranchName        string
	Status            string
	IntegrationBranch string // from the workspace, not the patch
}

// RecoveryHook provides backup-ref reading, backup-ref removal and
// reset-to-origin operations. It decouples the workspace handlers from
// the carrypatch package, following the same pattern as BranchResolverFunc
// and CarryPatchSyncFunc.
type RecoveryHook interface {
	// ReadReplacedSHA returns the SHA that refs/hub/replaced/<branch>
	// resolves to. found is false when the ref does not exist or the
	// trunk is not on disk. An error means the lookup itself failed.
	ReadReplacedSHA(ctx context.Context, slug, branch string) (sha string, found bool, err error)

	// RemoveBackup deletes refs/hub/replaced/<branch>. A missing ref
	// or a missing trunk is not an error.
	RemoveBackup(ctx context.Context, slug, branch string) error

	// RunReset moves a patch branch to the fork's current tip.
	// It returns a ResetResult on success or a *ResetError on failure.
	RunReset(ctx context.Context, slug string, patch ResetPatchInfo, auth *apikit.AuthInfo) (ResetResult, error)
}

// recoveryHook is the registered recovery hook. When non-nil, the GET
// single-patch handler uses it to read replaced_sha, the DELETE handler
// uses it to clean up backup refs, and the reset handler delegates to it.
var recoveryHook RecoveryHook

// RegisterRecoveryHook registers a RecoveryHook implementation.
// Called from main.go during server initialization.
func RegisterRecoveryHook(hook RecoveryHook) {
	recoveryHook = hook
}

// getRecoveryHook returns the currently registered recovery hook, or nil.
func getRecoveryHook() RecoveryHook {
	return recoveryHook
}
