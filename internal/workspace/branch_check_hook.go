package workspace

// BranchCheckFunc verifies that a git branch exists in the workspace repository.
// It receives the workspace slug and branch name, and returns nil if the branch
// exists. On error (branch not found or inaccessible repo), it returns a non-nil
// error.
//
// This hook decouples the git operations from the workspace package, avoiding
// an import cycle while allowing branch validation at patch registration time.
type BranchCheckFunc func(slug, branchName string) error

// branchCheckHook is the registered branch-check handler. When non-nil,
// handleAddPatch calls it to validate that the branch exists before inserting.
var branchCheckHook BranchCheckFunc

// RegisterBranchCheckHook registers a function to validate branch existence
// in workspace repositories. Called from main.go during server initialization.
func RegisterBranchCheckHook(fn BranchCheckFunc) {
	branchCheckHook = fn
}
