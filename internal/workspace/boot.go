package workspace

import (
	"fmt"
	"os"
)

// EnsureWorkspaceRoot creates the workspace root directory (and any missing
// parent directories) if it does not already exist. It is called during
// server boot to ensure clone operations can succeed.
//
// Returns nil on success. Returns an error if the directory cannot be created
// due to insufficient permissions or other OS error.
func EnsureWorkspaceRoot(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("failed to create workspace root %q: %w", path, err)
	}
	return nil
}
