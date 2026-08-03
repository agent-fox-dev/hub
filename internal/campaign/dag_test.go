package campaign

import (
	"context"
	"strings"
	"testing"
)

// TS-12-9: DAG builder reads the dependencies array from each spec's tasks.json
// and builds a directed graph with spec IDs as vertices and dependency
// relationships as edges.
func TestBuildDAG_LinearDependency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reader := newMockTasksReader()
	reader.tasks["07"] = &TasksJSON{Dependencies: nil} // no deps
	reader.tasks["09"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{
				DependsOnSpec: "07",
				FromGroup:     3,
				ToGroup:       1,
				Relationship:  "Uses secrets Store",
				Sentinel:      false,
			},
		},
	}

	dag, err := BuildDAG(ctx, []string{"07", "09"}, reader)
	if err != nil {
		t.Fatalf("BuildDAG() returned error: %v", err)
	}
	if dag == nil {
		t.Fatal("BuildDAG() returned nil DAG")
	}

	// Verify specs.
	if len(dag.Specs) != 2 {
		t.Fatalf("dag.Specs length = %d; want 2", len(dag.Specs))
	}

	// Verify edges.
	if len(dag.Edges) != 1 {
		t.Fatalf("dag.Edges length = %d; want 1", len(dag.Edges))
	}
	edge := dag.Edges[0]
	if edge.From != "07" {
		t.Errorf("edge.From = %q; want %q", edge.From, "07")
	}
	if edge.To != "09" {
		t.Errorf("edge.To = %q; want %q", edge.To, "09")
	}
	if edge.Relationship != "Uses secrets Store" {
		t.Errorf("edge.Relationship = %q; want %q", edge.Relationship, "Uses secrets Store")
	}
}

// TS-12-10: DAG builder detects a cycle in spec dependencies and returns an
// error describing the cycle.
func TestBuildDAG_CycleDetection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reader := newMockTasksReader()
	reader.tasks["07"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "09", Relationship: "depends on 09"},
		},
	}
	reader.tasks["09"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "07", Relationship: "depends on 07"},
		},
	}

	dag, err := BuildDAG(ctx, []string{"07", "09"}, reader)
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention 'cycle', got: %v", err)
	}
	if dag != nil {
		t.Errorf("expected nil DAG on cycle error, got: %+v", dag)
	}
}

// TS-12-11: DAG builder treats dependency entries referencing specs outside the
// campaign as pre-satisfied, not as an error.
func TestBuildDAG_ExternalDependencyPreSatisfied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reader := newMockTasksReader()
	reader.tasks["09"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "05", Relationship: "uses feature from 05"},
		},
	}
	// Spec "05" is NOT in the campaign — should be treated as pre-satisfied.

	dag, err := BuildDAG(ctx, []string{"09"}, reader)
	if err != nil {
		t.Fatalf("BuildDAG() returned error: %v", err)
	}
	if dag == nil {
		t.Fatal("BuildDAG() returned nil DAG")
	}

	// Spec 09 should be in the frontier since its only dependency is external.
	frontier := ComputeFrontier(dag, map[string]bool{})
	found := false
	for _, s := range frontier {
		if s == "09" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("spec '09' should be in frontier (external dep pre-satisfied), frontier = %v", frontier)
	}
}

// TS-12-12: DAG builder stores only spec-level edges (from, to, relationship)
// in the dag column; discards from_group, to_group, and sentinel fields.
func TestBuildDAG_DiscardsGroupAndSentinelFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reader := newMockTasksReader()
	reader.tasks["07"] = &TasksJSON{Dependencies: nil}
	reader.tasks["09"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{
				DependsOnSpec: "07",
				FromGroup:     3,
				ToGroup:       1,
				Relationship:  "Uses secrets",
				Sentinel:      true,
			},
		},
	}

	dag, err := BuildDAG(ctx, []string{"07", "09"}, reader)
	if err != nil {
		t.Fatalf("BuildDAG() returned error: %v", err)
	}
	if dag == nil {
		t.Fatal("BuildDAG() returned nil DAG")
	}

	// Serialize the DAG and verify no from_group, to_group, or sentinel fields.
	dagJSON, err := SerializeDAG(dag)
	if err != nil {
		t.Fatalf("SerializeDAG() returned error: %v", err)
	}
	if dagJSON == "" {
		t.Fatal("SerializeDAG() returned empty string")
	}
	if strings.Contains(dagJSON, "from_group") {
		t.Error("serialized DAG should not contain 'from_group'")
	}
	if strings.Contains(dagJSON, "to_group") {
		t.Error("serialized DAG should not contain 'to_group'")
	}
	if strings.Contains(dagJSON, "sentinel") {
		t.Error("serialized DAG should not contain 'sentinel'")
	}
	if !strings.Contains(dagJSON, "relationship") {
		t.Error("serialized DAG should contain 'relationship'")
	}
}

// TS-12-13: DAG builder treats the sentinel field in tasks.json dependency
// entries as having no effect on edge construction or scheduling.
func TestBuildDAG_SentinelFieldHasNoEffect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Build DAG with sentinel=true.
	readerWithSentinel := newMockTasksReader()
	readerWithSentinel.tasks["07"] = &TasksJSON{Dependencies: nil}
	readerWithSentinel.tasks["09"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "07", Relationship: "Uses secrets", Sentinel: true},
		},
	}

	dagSentinel, err := BuildDAG(ctx, []string{"07", "09"}, readerWithSentinel)
	if err != nil {
		t.Fatalf("BuildDAG(sentinel=true) returned error: %v", err)
	}
	if dagSentinel == nil {
		t.Fatal("BuildDAG(sentinel=true) returned nil DAG")
	}

	// Build DAG with sentinel=false.
	readerNoSentinel := newMockTasksReader()
	readerNoSentinel.tasks["07"] = &TasksJSON{Dependencies: nil}
	readerNoSentinel.tasks["09"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "07", Relationship: "Uses secrets", Sentinel: false},
		},
	}

	dagNoSentinel, err := BuildDAG(ctx, []string{"07", "09"}, readerNoSentinel)
	if err != nil {
		t.Fatalf("BuildDAG(sentinel=false) returned error: %v", err)
	}
	if dagNoSentinel == nil {
		t.Fatal("BuildDAG(sentinel=false) returned nil DAG")
	}

	// Edges should be identical regardless of sentinel value.
	if len(dagSentinel.Edges) != len(dagNoSentinel.Edges) {
		t.Errorf("edge count differs: sentinel=%d, no-sentinel=%d",
			len(dagSentinel.Edges), len(dagNoSentinel.Edges))
	}
	if len(dagSentinel.Edges) > 0 && len(dagNoSentinel.Edges) > 0 {
		if dagSentinel.Edges[0].From != dagNoSentinel.Edges[0].From {
			t.Errorf("edge.From differs: sentinel=%q, no-sentinel=%q",
				dagSentinel.Edges[0].From, dagNoSentinel.Edges[0].From)
		}
		if dagSentinel.Edges[0].To != dagNoSentinel.Edges[0].To {
			t.Errorf("edge.To differs: sentinel=%q, no-sentinel=%q",
				dagSentinel.Edges[0].To, dagNoSentinel.Edges[0].To)
		}
	}
}

// Additional test: diamond dependency shape (A→B, A→C, B→D, C→D).
func TestBuildDAG_DiamondDependency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reader := newMockTasksReader()
	reader.tasks["A"] = &TasksJSON{Dependencies: nil}
	reader.tasks["B"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "A", Relationship: "B depends on A"},
		},
	}
	reader.tasks["C"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "A", Relationship: "C depends on A"},
		},
	}
	reader.tasks["D"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "B", Relationship: "D depends on B"},
			{DependsOnSpec: "C", Relationship: "D depends on C"},
		},
	}

	dag, err := BuildDAG(ctx, []string{"A", "B", "C", "D"}, reader)
	if err != nil {
		t.Fatalf("BuildDAG(diamond) returned error: %v", err)
	}
	if dag == nil {
		t.Fatal("BuildDAG(diamond) returned nil DAG")
	}

	// Should have 4 specs and 4 edges.
	if len(dag.Specs) != 4 {
		t.Errorf("dag.Specs length = %d; want 4", len(dag.Specs))
	}
	if len(dag.Edges) != 4 {
		t.Errorf("dag.Edges length = %d; want 4", len(dag.Edges))
	}

	// Initial frontier should only contain A.
	frontier := ComputeFrontier(dag, map[string]bool{})
	if len(frontier) != 1 {
		t.Errorf("initial frontier length = %d; want 1 (just 'A')", len(frontier))
	}
	if len(frontier) > 0 && frontier[0] != "A" {
		t.Errorf("initial frontier[0] = %q; want %q", frontier[0], "A")
	}

	// After A merges, frontier should contain B and C.
	frontierAfterA := ComputeFrontier(dag, map[string]bool{"A": true})
	if len(frontierAfterA) != 2 {
		t.Errorf("frontier after A merged length = %d; want 2 (B and C)", len(frontierAfterA))
	}
}

// Additional test: linear chain A→B→C.
func TestBuildDAG_LinearChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reader := newMockTasksReader()
	reader.tasks["A"] = &TasksJSON{Dependencies: nil}
	reader.tasks["B"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "A", Relationship: "B depends on A"},
		},
	}
	reader.tasks["C"] = &TasksJSON{
		Dependencies: []TaskDependency{
			{DependsOnSpec: "B", Relationship: "C depends on B"},
		},
	}

	dag, err := BuildDAG(ctx, []string{"A", "B", "C"}, reader)
	if err != nil {
		t.Fatalf("BuildDAG(chain) returned error: %v", err)
	}
	if dag == nil {
		t.Fatal("BuildDAG(chain) returned nil DAG")
	}

	if len(dag.Specs) != 3 {
		t.Errorf("dag.Specs length = %d; want 3", len(dag.Specs))
	}
	if len(dag.Edges) != 2 {
		t.Errorf("dag.Edges length = %d; want 2", len(dag.Edges))
	}

	// Initial frontier: only A.
	frontier := ComputeFrontier(dag, map[string]bool{})
	if len(frontier) != 1 {
		t.Errorf("initial frontier length = %d; want 1", len(frontier))
	}
}
