package campaign

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
	Dependencies []TaskDependency     `json:"dependencies"`
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
func DiscoverPendingSpecs(_ context.Context, workspaceRoot, slug string) ([]string, []string, error) {
	specsDir := filepath.Join(workspaceRoot, slug, "trunk", ".agent-fox", "specs")

	entries, err := os.ReadDir(specsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("workspace directory not found: %s", specsDir)
		}
		return nil, nil, fmt.Errorf("failed to read specs directory: %w", err)
	}

	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("no spec directories found at %s", specsDir)
	}

	// Filter for directories only.
	var specDirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			specDirs = append(specDirs, e)
		}
	}

	if len(specDirs) == 0 {
		return nil, nil, fmt.Errorf("no spec directories found at %s", specsDir)
	}

	var specIDs []string
	var warnings []string

	for _, dir := range specDirs {
		dirName := dir.Name()
		specID := ExtractSpecID(dirName)
		if specID == "" {
			continue
		}

		tasksPath := filepath.Join(specsDir, dirName, "tasks.json")
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			// Missing tasks.json — silently skip (12-REQ-3.E1).
			continue
		}

		var tj DiscoveryTasksJSON
		if err := json.Unmarshal(data, &tj); err != nil {
			// Malformed tasks.json — skip with warning (12-REQ-3.E2).
			warnings = append(warnings, fmt.Sprintf("spec %s: malformed tasks.json: %v", specID, err))
			continue
		}

		if HasPendingSubtask(&tj) {
			specIDs = append(specIDs, specID)
		}
	}

	if len(specIDs) == 0 {
		return nil, warnings, fmt.Errorf("no specs with pending subtasks found")
	}

	sort.Strings(specIDs)
	return specIDs, warnings, nil
}

// HasPendingSubtask reports whether a tasks.json has at least one subtask
// with state equal to "pending".
func HasPendingSubtask(tj *DiscoveryTasksJSON) bool {
	if tj == nil {
		return false
	}
	for _, tg := range tj.Tasks {
		for _, st := range tg.Subtasks {
			if st.State == "pending" {
				return true
			}
		}
	}
	return false
}

// ExtractSpecID extracts the spec ID (numeric prefix) from a spec directory name.
// For example, "07_secrets_variables" returns "07".
func ExtractSpecID(dirName string) string {
	idx := strings.IndexByte(dirName, '_')
	if idx <= 0 {
		return ""
	}
	return dirName[:idx]
}

// readDirSafe reads a directory and returns its entries, or an empty slice on error.
func readDirSafe(dir string) ([]os.DirEntry, error) {
	return os.ReadDir(dir)
}

// readFileSafe reads a file and returns its contents, or an error if not readable.
func readFileSafe(path string) ([]byte, error) {
	return os.ReadFile(path)
}
