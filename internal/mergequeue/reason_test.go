package mergequeue

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Subtask 2.1: CantMergeReason enum constants
// Requirement: 11-REQ-3.1 (enum is part of CanMerge's return contract)
// ---------------------------------------------------------------------------

func TestCantMergeReason_AllConstantsDefined(t *testing.T) {
	// All five CantMergeReason constants must be defined and non-empty.
	constants := []struct {
		name  string
		value CantMergeReason
	}{
		{"BeforeDependency", BeforeDependency},
		{"WouldConflict", WouldConflict},
		{"AlreadyMerged", AlreadyMerged},
		{"BranchNotReady", BranchNotReady},
		{"SpecBlocked", SpecBlocked},
	}

	for _, c := range constants {
		t.Run(c.name, func(t *testing.T) {
			if c.value == "" {
				t.Errorf("CantMergeReason constant %s is empty string", c.name)
			}
		})
	}
}

func TestCantMergeReason_StringRepresentation(t *testing.T) {
	// Each constant's string representation must match the expected value.
	tests := []struct {
		reason CantMergeReason
		want   string
	}{
		{BeforeDependency, "BeforeDependency"},
		{WouldConflict, "WouldConflict"},
		{AlreadyMerged, "AlreadyMerged"},
		{BranchNotReady, "BranchNotReady"},
		{SpecBlocked, "SpecBlocked"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := string(tc.reason)
			if got != tc.want {
				t.Errorf("string(%v) = %q; want %q", tc.reason, got, tc.want)
			}
		})
	}
}

func TestCantMergeReason_TypeSafety(t *testing.T) {
	// CantMergeReason is a typed string — not a raw string alias.
	// This test verifies typed assignment and zero-value behavior.
	var reason CantMergeReason = BeforeDependency
	if reason != BeforeDependency {
		t.Errorf("typed assignment: got %v, want %v", reason, BeforeDependency)
	}

	// The zero value (empty CantMergeReason) represents "no reason".
	var empty CantMergeReason
	if empty != "" {
		t.Errorf("zero value of CantMergeReason = %q; want empty string", empty)
	}
}

func TestCantMergeReason_Uniqueness(t *testing.T) {
	// All five constants must have distinct values.
	reasons := []CantMergeReason{
		BeforeDependency,
		WouldConflict,
		AlreadyMerged,
		BranchNotReady,
		SpecBlocked,
	}

	seen := make(map[CantMergeReason]bool)
	for _, r := range reasons {
		if seen[r] {
			t.Errorf("duplicate CantMergeReason value: %q", r)
		}
		seen[r] = true
	}
}
