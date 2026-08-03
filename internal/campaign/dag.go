package campaign

import "context"

// TasksReader reads tasks.json files for spec directories.
type TasksReader interface {
	ReadTasksJSON(ctx context.Context, specID string) (*TasksJSON, error)
}

// TasksJSON represents the relevant parts of a tasks.json file for DAG construction.
type TasksJSON struct {
	Dependencies []TaskDependency `json:"dependencies"`
}

// TaskDependency represents a dependency entry from tasks.json.
type TaskDependency struct {
	DependsOnSpec string `json:"depends_on_spec"`
	FromGroup     int    `json:"from_group,omitempty"`
	ToGroup       int    `json:"to_group,omitempty"`
	Relationship  string `json:"relationship"`
	Sentinel      bool   `json:"sentinel,omitempty"`
}

// BuildDAG constructs and validates a dependency DAG from tasks.json files.
// Returns an error if a cycle is detected.
func BuildDAG(_ context.Context, _ []string, _ TasksReader) (*DAG, error) {
	return nil, nil // stub
}

// ComputeFrontier returns spec IDs whose upstream dependencies are all satisfied.
func ComputeFrontier(_ *DAG, _ map[string]bool) []string {
	return nil // stub
}

// SerializeDAG converts a DAG to its JSON representation for storage.
func SerializeDAG(_ *DAG) (string, error) {
	return "", nil // stub
}

// DeserializeDAG converts JSON from the dag column back to a DAG struct.
func DeserializeDAG(_ string) (*DAG, error) {
	return nil, nil // stub
}

// SerializeConflictDetails converts a list of file paths to JSON for storage.
func SerializeConflictDetails(_ []string) (string, error) {
	return "", nil // stub
}

// DeserializeConflictDetails converts JSON from the conflict_details column
// back to a string slice.
func DeserializeConflictDetails(_ string) ([]string, error) {
	return nil, nil // stub
}
