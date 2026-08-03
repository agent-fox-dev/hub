package campaign

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

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
// It reads the dependencies array from each spec's tasks.json, extracts
// depends_on_spec fields, and builds a directed graph. Only edges between
// specs within the campaign are included; external dependencies are treated
// as pre-satisfied. The sentinel field has no effect on edge construction.
// Returns an error if a cycle is detected.
func BuildDAG(ctx context.Context, specIDs []string, reader TasksReader) (*DAG, error) {
	specSet := make(map[string]bool, len(specIDs))
	for _, id := range specIDs {
		specSet[id] = true
	}

	dag := &DAG{
		Specs: make([]string, len(specIDs)),
		Edges: []Edge{},
	}
	copy(dag.Specs, specIDs)

	// Deduplicate edges: key is "from→to".
	seenEdges := make(map[string]bool)

	for _, specID := range specIDs {
		tj, err := reader.ReadTasksJSON(ctx, specID)
		if err != nil {
			// Spec has no readable tasks.json — include it with no dependencies.
			continue
		}
		for _, dep := range tj.Dependencies {
			if dep.DependsOnSpec == "" {
				continue
			}
			// Only include edges to specs within the campaign.
			// External dependencies are treated as pre-satisfied.
			if !specSet[dep.DependsOnSpec] {
				continue
			}
			edgeKey := dep.DependsOnSpec + "→" + specID
			if seenEdges[edgeKey] {
				continue
			}
			seenEdges[edgeKey] = true

			// Store only spec-level edges: from, to, relationship.
			// Discard from_group, to_group, and sentinel.
			dag.Edges = append(dag.Edges, Edge{
				From:         dep.DependsOnSpec,
				To:           specID,
				Relationship: dep.Relationship,
			})
		}
	}

	// Validate acyclicity using Kahn's algorithm (topological sort).
	if err := validateAcyclic(dag); err != nil {
		return nil, err
	}

	return dag, nil
}

// validateAcyclic checks that the DAG has no cycles using Kahn's algorithm.
func validateAcyclic(dag *DAG) error {
	// Build adjacency list and in-degree map.
	inDegree := make(map[string]int)
	adj := make(map[string][]string)
	for _, s := range dag.Specs {
		inDegree[s] = 0
	}
	for _, e := range dag.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		inDegree[e.To]++
	}

	// Seed queue with nodes having in-degree 0.
	var queue []string
	for _, s := range dag.Specs {
		if inDegree[s] == 0 {
			queue = append(queue, s)
		}
	}

	processed := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		processed++
		for _, neighbor := range adj[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if processed != len(dag.Specs) {
		return fmt.Errorf("cycle detected in spec dependency graph")
	}
	return nil
}

// ComputeFrontier returns spec IDs whose upstream dependencies are all
// satisfied (merged or pre-satisfied). A spec is in the frontier if every
// edge pointing to it has its "from" spec in the mergedSpecs set.
func ComputeFrontier(dag *DAG, mergedSpecs map[string]bool) []string {
	if dag == nil {
		return nil
	}

	// Build set of specs with unmet dependencies.
	blocked := make(map[string]bool)
	for _, e := range dag.Edges {
		if !mergedSpecs[e.From] {
			blocked[e.To] = true
		}
	}

	var frontier []string
	for _, s := range dag.Specs {
		// Skip already-merged specs.
		if mergedSpecs[s] {
			continue
		}
		if !blocked[s] {
			frontier = append(frontier, s)
		}
	}
	return frontier
}

// TopologicalOrder returns spec IDs in topological order (upstream specs
// first). Used to determine rebase order.
func TopologicalOrder(dag *DAG) []string {
	if dag == nil {
		return nil
	}

	inDegree := make(map[string]int)
	adj := make(map[string][]string)
	for _, s := range dag.Specs {
		inDegree[s] = 0
	}
	for _, e := range dag.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		inDegree[e.To]++
	}

	var queue []string
	for _, s := range dag.Specs {
		if inDegree[s] == 0 {
			queue = append(queue, s)
		}
	}
	// Sort for deterministic ordering.
	sort.Strings(queue)

	var order []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)
		for _, neighbor := range adj[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
		sort.Strings(queue)
	}
	return order
}

// SerializeDAG converts a DAG to its JSON representation for storage.
// The output shape is {"specs":[...], "edges":[{"from":"...","to":"...","relationship":"..."}]}.
func SerializeDAG(dag *DAG) (string, error) {
	if dag == nil {
		return "{}", nil
	}
	data, err := json.Marshal(dag)
	if err != nil {
		return "", fmt.Errorf("serialize DAG: %w", err)
	}
	return string(data), nil
}

// DeserializeDAG converts JSON from the dag column back to a DAG struct.
// Returns a non-nil error if the JSON is malformed.
func DeserializeDAG(s string) (*DAG, error) {
	var dag DAG
	if err := json.Unmarshal([]byte(s), &dag); err != nil {
		return nil, fmt.Errorf("deserialize DAG: %w", err)
	}
	return &dag, nil
}

// SerializeConflictDetails converts a list of file paths to JSON for storage.
func SerializeConflictDetails(details []string) (string, error) {
	if details == nil {
		details = []string{}
	}
	data, err := json.Marshal(details)
	if err != nil {
		return "", fmt.Errorf("serialize conflict details: %w", err)
	}
	return string(data), nil
}

// DeserializeConflictDetails converts JSON from the conflict_details column
// back to a string slice. Returns a non-nil error if the JSON is malformed.
func DeserializeConflictDetails(s string) ([]string, error) {
	var details []string
	if err := json.Unmarshal([]byte(s), &details); err != nil {
		return nil, fmt.Errorf("deserialize conflict details: %w", err)
	}
	return details, nil
}
