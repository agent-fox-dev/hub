package campaign

import (
	"strings"
	"testing"
)

// TS-12-48: Store module serializes the DAG as JSON with shape
// {specs, edges: [{from, to, relationship}]} into the dag TEXT column and
// deserializes it back to the same shape for API responses.
func TestSerializeDAG_RoundTrip(t *testing.T) {
	t.Parallel()

	dag := &DAG{
		Specs: []string{"07", "09"},
		Edges: []Edge{
			{From: "07", To: "09", Relationship: "Uses secrets Store"},
		},
	}

	// Serialize.
	dagJSON, err := SerializeDAG(dag)
	if err != nil {
		t.Fatalf("SerializeDAG() returned error: %v", err)
	}
	if dagJSON == "" {
		t.Fatal("SerializeDAG() returned empty string")
	}

	// Verify JSON structure contains expected fields.
	if !strings.Contains(dagJSON, `"specs"`) {
		t.Error("serialized DAG should contain '\"specs\"'")
	}
	if !strings.Contains(dagJSON, `"edges"`) {
		t.Error("serialized DAG should contain '\"edges\"'")
	}
	if !strings.Contains(dagJSON, `"from"`) {
		t.Error("serialized DAG should contain '\"from\"'")
	}
	if !strings.Contains(dagJSON, `"to"`) {
		t.Error("serialized DAG should contain '\"to\"'")
	}
	if !strings.Contains(dagJSON, `"relationship"`) {
		t.Error("serialized DAG should contain '\"relationship\"'")
	}

	// Deserialize.
	deserialized, err := DeserializeDAG(dagJSON)
	if err != nil {
		t.Fatalf("DeserializeDAG() returned error: %v", err)
	}
	if deserialized == nil {
		t.Fatal("DeserializeDAG() returned nil")
	}

	// Verify round-trip fidelity.
	if len(deserialized.Specs) != 2 {
		t.Fatalf("deserialized Specs length = %d; want 2", len(deserialized.Specs))
	}
	if deserialized.Specs[0] != "07" {
		t.Errorf("deserialized Specs[0] = %q; want %q", deserialized.Specs[0], "07")
	}
	if deserialized.Specs[1] != "09" {
		t.Errorf("deserialized Specs[1] = %q; want %q", deserialized.Specs[1], "09")
	}
	if len(deserialized.Edges) != 1 {
		t.Fatalf("deserialized Edges length = %d; want 1", len(deserialized.Edges))
	}
	if deserialized.Edges[0].From != "07" {
		t.Errorf("deserialized Edges[0].From = %q; want %q", deserialized.Edges[0].From, "07")
	}
	if deserialized.Edges[0].To != "09" {
		t.Errorf("deserialized Edges[0].To = %q; want %q", deserialized.Edges[0].To, "09")
	}
	if deserialized.Edges[0].Relationship != "Uses secrets Store" {
		t.Errorf("deserialized Edges[0].Relationship = %q; want %q",
			deserialized.Edges[0].Relationship, "Uses secrets Store")
	}
}

// TS-12-48 supplement: Verify that serialized DAG does not contain from_group,
// to_group, or sentinel fields.
func TestSerializeDAG_ExcludesInternalFields(t *testing.T) {
	t.Parallel()

	dag := &DAG{
		Specs: []string{"07", "09"},
		Edges: []Edge{
			{From: "07", To: "09", Relationship: "Uses secrets"},
		},
	}

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
}

// TS-12-16.E1: If dag column contains malformed JSON, return non-nil error.
func TestDeserializeDAG_MalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := DeserializeDAG("{invalid json")
	if err == nil {
		t.Fatal("expected error for malformed DAG JSON, got nil")
	}
}

// TS-12-49: Store module serializes conflict_details as a JSON array of file
// path strings and deserializes it directly into the API response array.
func TestSerializeConflictDetails_RoundTrip(t *testing.T) {
	t.Parallel()

	details := []string{"file1.go", "file2.go"}

	// Serialize.
	serialized, err := SerializeConflictDetails(details)
	if err != nil {
		t.Fatalf("SerializeConflictDetails() returned error: %v", err)
	}
	if serialized == "" {
		t.Fatal("SerializeConflictDetails() returned empty string")
	}

	// Verify JSON shape.
	expected := `["file1.go","file2.go"]`
	if serialized != expected {
		t.Errorf("serialized = %q; want %q", serialized, expected)
	}

	// Deserialize.
	deserialized, err := DeserializeConflictDetails(serialized)
	if err != nil {
		t.Fatalf("DeserializeConflictDetails() returned error: %v", err)
	}
	if len(deserialized) != 2 {
		t.Fatalf("deserialized length = %d; want 2", len(deserialized))
	}
	if deserialized[0] != "file1.go" {
		t.Errorf("deserialized[0] = %q; want %q", deserialized[0], "file1.go")
	}
	if deserialized[1] != "file2.go" {
		t.Errorf("deserialized[1] = %q; want %q", deserialized[1], "file2.go")
	}
}

// TS-12-16.E2: If conflict_details column contains malformed JSON, return non-nil error.
func TestDeserializeConflictDetails_MalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := DeserializeConflictDetails("{not an array}")
	if err == nil {
		t.Fatal("expected error for malformed conflict_details JSON, got nil")
	}
}

// Additional: empty conflict_details serializes correctly.
func TestSerializeConflictDetails_Empty(t *testing.T) {
	t.Parallel()

	serialized, err := SerializeConflictDetails([]string{})
	if err != nil {
		t.Fatalf("SerializeConflictDetails() returned error: %v", err)
	}
	if serialized == "" {
		t.Fatal("SerializeConflictDetails() returned empty string for empty slice")
	}

	deserialized, err := DeserializeConflictDetails(serialized)
	if err != nil {
		t.Fatalf("DeserializeConflictDetails() returned error: %v", err)
	}
	if len(deserialized) != 0 {
		t.Errorf("deserialized length = %d; want 0", len(deserialized))
	}
}
