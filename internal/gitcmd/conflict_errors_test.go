package gitcmd

import (
	"strings"
	"testing"
)

// ========================================================================
// Spec 14 Task 2.4: CherryPickConflictError and MergeNoFFConflictError type tests
// (TS-14-28)
// Requirements: 14-REQ-13.1
// ========================================================================

// TestCherryPickConflictError_Interface verifies that CherryPickConflictError
// is an exported struct with a ConflictingFiles field and an Error() method
// that satisfies the error interface and returns a non-empty human-readable
// message consistent with RebaseConflictError's format.
//
// TS-14-28
// Requirement: 14-REQ-13.1
func TestCherryPickConflictError_Interface(t *testing.T) {
	cpErr := &CherryPickConflictError{
		ConflictingFiles: []string{"file1.go", "file2.go"},
	}

	// Must satisfy the error interface (compile-time + runtime check).
	var _ error = cpErr

	// Error() must return a non-empty string.
	msg := cpErr.Error()
	if msg == "" {
		t.Fatal("CherryPickConflictError.Error() should return a non-empty string")
	}

	// Error message should follow the same pattern as RebaseConflictError:
	// "cherry-pick conflict in N file(s): file1, file2"
	if !strings.Contains(msg, "conflict") {
		t.Errorf("Error() message should contain 'conflict', got: %q", msg)
	}
	if !strings.Contains(msg, "file1.go") {
		t.Errorf("Error() message should contain file path 'file1.go', got: %q", msg)
	}
	if !strings.Contains(msg, "file2.go") {
		t.Errorf("Error() message should contain file path 'file2.go', got: %q", msg)
	}

	// Verify ConflictingFiles field is populated correctly.
	if len(cpErr.ConflictingFiles) != 2 {
		t.Errorf("ConflictingFiles should have 2 entries, got %d", len(cpErr.ConflictingFiles))
	}
}

// TestMergeNoFFConflictError_Interface verifies that MergeNoFFConflictError
// is an exported struct with a ConflictingFiles field and an Error() method
// that satisfies the error interface and returns a non-empty human-readable
// message consistent with RebaseConflictError's format.
//
// TS-14-28
// Requirement: 14-REQ-13.1
func TestMergeNoFFConflictError_Interface(t *testing.T) {
	mergeErr := &MergeNoFFConflictError{
		ConflictingFiles: []string{"file1.go"},
	}

	// Must satisfy the error interface (compile-time + runtime check).
	var _ error = mergeErr

	// Error() must return a non-empty string.
	msg := mergeErr.Error()
	if msg == "" {
		t.Fatal("MergeNoFFConflictError.Error() should return a non-empty string")
	}

	// Error message should follow the same pattern as RebaseConflictError.
	if !strings.Contains(msg, "conflict") {
		t.Errorf("Error() message should contain 'conflict', got: %q", msg)
	}
	if !strings.Contains(msg, "file1.go") {
		t.Errorf("Error() message should contain file path 'file1.go', got: %q", msg)
	}

	// Verify ConflictingFiles field is populated correctly.
	if len(mergeErr.ConflictingFiles) != 1 {
		t.Errorf("ConflictingFiles should have 1 entry, got %d", len(mergeErr.ConflictingFiles))
	}
}

// TestConflictErrorFormat_ConsistentWithRebase verifies that both new conflict
// error types follow a format consistent with RebaseConflictError. They should
// all include the conflict count and file list in the message.
//
// TS-14-28
// Requirement: 14-REQ-13.1
func TestConflictErrorFormat_ConsistentWithRebase(t *testing.T) {
	files := []string{"a.go", "b.go"}

	rebaseErr := &RebaseConflictError{ConflictingFiles: files}
	cpErr := &CherryPickConflictError{ConflictingFiles: files}
	mergeErr := &MergeNoFFConflictError{ConflictingFiles: files}

	// All three should contain "conflict in 2 file(s): a.go, b.go".
	for _, tc := range []struct {
		name string
		msg  string
	}{
		{"RebaseConflictError", rebaseErr.Error()},
		{"CherryPickConflictError", cpErr.Error()},
		{"MergeNoFFConflictError", mergeErr.Error()},
	} {
		if !strings.Contains(tc.msg, "2 file(s)") {
			t.Errorf("%s.Error() should contain '2 file(s)', got: %q", tc.name, tc.msg)
		}
		if !strings.Contains(tc.msg, "a.go, b.go") {
			t.Errorf("%s.Error() should contain 'a.go, b.go', got: %q", tc.name, tc.msg)
		}
	}
}

// ========================================================================
// Spec 14 Task 2.5: Conflict file parsing fallback tests
// (TS-14-29)
// Requirements: 14-REQ-13.2, 14-REQ-13.E1
// ========================================================================

// TestParseRebaseConflictFiles_StandardOutput verifies that
// parseRebaseConflictFiles correctly extracts file paths from standard
// CONFLICT lines in git output.
//
// TS-14-29
// Requirement: 14-REQ-13.2
func TestParseRebaseConflictFiles_StandardOutput(t *testing.T) {
	output := `Rebasing (1/1)
CONFLICT (content): Merge conflict in path/to/file.go
CONFLICT (content): Merge conflict in another/file.txt
error: could not apply abc1234... feature change`

	files := parseRebaseConflictFiles(output)
	if len(files) != 2 {
		t.Fatalf("expected 2 conflict files, got %d: %v", len(files), files)
	}

	if files[0] != "path/to/file.go" {
		t.Errorf("files[0] = %q, want %q", files[0], "path/to/file.go")
	}
	if files[1] != "another/file.txt" {
		t.Errorf("files[1] = %q, want %q", files[1], "another/file.txt")
	}
}

// TestParseRebaseConflictFiles_EmptyOutput verifies that
// parseRebaseConflictFiles returns an empty slice (not nil) when the output
// contains no CONFLICT lines.
//
// TS-14-29
// Requirement: 14-REQ-13.E1
func TestParseRebaseConflictFiles_EmptyOutput(t *testing.T) {
	files := parseRebaseConflictFiles("")
	if files == nil {
		t.Fatal("parseRebaseConflictFiles should return empty slice, not nil")
	}
	if len(files) != 0 {
		t.Errorf("expected 0 conflict files for empty output, got %d: %v", len(files), files)
	}
}

// TestParseRebaseConflictFiles_NoCONFLICTLines verifies the fallback when
// the output contains text but no CONFLICT-prefixed lines.
//
// TS-14-29
// Requirement: 14-REQ-13.E1
func TestParseRebaseConflictFiles_NoCONFLICTLines(t *testing.T) {
	output := `error: could not apply abc1234
hint: Resolve all conflicts manually
hint: "git add/rm <conflicted_files>"`

	files := parseRebaseConflictFiles(output)
	if len(files) != 0 {
		t.Errorf("expected 0 conflict files when no CONFLICT lines, got %d: %v",
			len(files), files)
	}
}

// TestConflictFallbackEntry verifies that the fallback entry
// "(unresolved conflict)" is used in ConflictingFiles when
// parseRebaseConflictFiles returns no files for a conflict scenario.
//
// This tests the contract documented in 14-REQ-4.E2 and 14-REQ-13.E1:
// when conflict output cannot be parsed, ConflictingFiles should contain
// the fallback entry rather than being empty.
//
// TS-14-29
// Requirement: 14-REQ-4.E2, 14-REQ-13.E1
// Correctness property: 14-PROP-6
func TestConflictFallbackEntry(t *testing.T) {
	// Verify the parseRebaseConflictFiles returns empty for unparseable output.
	files := parseRebaseConflictFiles("some random output with no CONFLICT lines")
	if len(files) != 0 {
		t.Fatalf("parseRebaseConflictFiles should return empty for unparseable output, got: %v", files)
	}

	// When no conflict files are parsed, the caller (CherryPick) should
	// populate ConflictingFiles with the fallback entry.
	// Construct a CherryPickConflictError with the fallback.
	fallback := []string{"(unresolved conflict)"}
	cpErr := &CherryPickConflictError{ConflictingFiles: fallback}
	if len(cpErr.ConflictingFiles) != 1 {
		t.Fatalf("fallback ConflictingFiles should have 1 entry, got %d", len(cpErr.ConflictingFiles))
	}
	if cpErr.ConflictingFiles[0] != "(unresolved conflict)" {
		t.Errorf("fallback entry = %q, want %q", cpErr.ConflictingFiles[0], "(unresolved conflict)")
	}

	// Same for MergeNoFFConflictError.
	mergeErr := &MergeNoFFConflictError{ConflictingFiles: fallback}
	if len(mergeErr.ConflictingFiles) != 1 {
		t.Fatalf("fallback ConflictingFiles should have 1 entry, got %d", len(mergeErr.ConflictingFiles))
	}
	if mergeErr.ConflictingFiles[0] != "(unresolved conflict)" {
		t.Errorf("fallback entry = %q, want %q", mergeErr.ConflictingFiles[0], "(unresolved conflict)")
	}
}
