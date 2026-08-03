package campaign

import "context"

// DiscoverySubtask represents a subtask entry with state for spec discovery.
type DiscoverySubtask struct {
	State string `json:"state"`
}

// DiscoveryTaskGroup represents a task group containing subtasks.
type DiscoveryTaskGroup struct {
	Subtasks []DiscoverySubtask `json:"subtasks"`
}

// DiscoveryTasksJSON represents the tasks.json structure needed for discovery.
// It extends the DAG-focused TasksJSON with subtask state information.
type DiscoveryTasksJSON struct {
	Dependencies []TaskDependency   `json:"dependencies"`
	Tasks        []DiscoveryTaskGroup `json:"tasks"`
}

// DiscoverPendingSpecs scans tasks.json files at
// <workspaceRoot>/<slug>/trunk/.agent-fox/specs/<spec_dir>/tasks.json
// and returns spec IDs that have at least one subtask with state=pending.
//
// Returns:
//   - specIDs: spec IDs with pending subtasks (e.g. ["07", "09"])
//   - warnings: human-readable strings for specs that were skipped
//   - err: non-nil if no spec directories exist or workspace is inaccessible
func DiscoverPendingSpecs(_ context.Context, _, _ string) ([]string, []string, error) {
	return nil, nil, nil // stub
}

// HasPendingSubtask reports whether a tasks.json has at least one subtask
// with state equal to "pending".
func HasPendingSubtask(_ *DiscoveryTasksJSON) bool {
	return false // stub
}

// ExtractSpecID extracts the spec ID (numeric prefix) from a spec directory name.
// For example, "07_secrets_variables" returns "07".
func ExtractSpecID(_ string) string {
	return "" // stub
}
