package gitcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ========================================================================
// Spec 11 Task 2.2: MergeTree clean merge and conflict tests
// (TS-11-15, TS-11-16, TS-11-17, TS-11-18)
// Requirements: 11-REQ-5.1, 11-REQ-5.2, 11-REQ-5.3, 11-REQ-5.4
// ========================================================================

// shaRegexp matches a 40-character lowercase hex string (git SHA).
var shaRegexp = regexp.MustCompile(`^[0-9a-f]{40}$`)

// TestMergeTree_CleanMerge verifies that MergeTree returns a valid tree SHA
// string and nil error when the merge between base and head is clean
// (no conflicts).
//
// TS-11-15
// Requirement: 11-REQ-5.1
func TestMergeTree_CleanMerge(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir, mainSHA, featureSHA := setupMergeableRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	treeSHA, err := runner.MergeTree(ctx, mainSHA, featureSHA)
	if err != nil {
		t.Fatalf("MergeTree returned unexpected error: %v", err)
	}
	if treeSHA == "" {
		t.Fatal("MergeTree returned empty tree SHA for clean merge")
	}
	if len(treeSHA) != 40 {
		t.Errorf("tree SHA length = %d, want 40: %q", len(treeSHA), treeSHA)
	}
	if !shaRegexp.MatchString(treeSHA) {
		t.Errorf("tree SHA %q does not match /^[0-9a-f]{40}$/", treeSHA)
	}
}

// TestMergeTree_ConflictDetection verifies that MergeTree returns a
// *MergeConflictError with ConflictingFiles populated when the merge
// between base and head produces conflicts.
//
// TS-11-16
// Requirement: 11-REQ-5.2
// Correctness property: 11-PROP-7
func TestMergeTree_ConflictDetection(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir, mainSHA, featureSHA := setupConflictingRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.MergeTree(ctx, mainSHA, featureSHA)
	if out != "" {
		t.Errorf("MergeTree should return empty string on conflict, got %q", out)
	}
	if err == nil {
		t.Fatal("MergeTree should return non-nil error on conflict")
	}

	// Extract *MergeConflictError using errors.As (11-REQ-5.2).
	var ce *MergeConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("error should be *MergeConflictError, got %T: %v", err, err)
	}

	// 11-PROP-7: ConflictingFiles must have length >= 1.
	if len(ce.ConflictingFiles) < 1 {
		t.Fatal("MergeConflictError.ConflictingFiles should have at least 1 entry")
	}

	// Verify "conflict.txt" is in the conflicting files list.
	found := false
	for _, f := range ce.ConflictingFiles {
		if f == "conflict.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ConflictingFiles %v should contain 'conflict.txt'", ce.ConflictingFiles)
	}
}

// TestMergeTree_ErrorsAsPattern verifies that callers can use errors.As
// to extract *MergeConflictError from the error returned by MergeTree,
// following the standard Go error inspection pattern.
//
// TS-11-16 (errors.As verification)
// Requirement: 11-REQ-5.2
func TestMergeTree_ErrorsAsPattern(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir, mainSHA, featureSHA := setupConflictingRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	_, err = runner.MergeTree(ctx, mainSHA, featureSHA)
	if err == nil {
		t.Fatal("expected non-nil error for conflicting merge")
	}

	// Verify errors.As works for *MergeConflictError.
	var ce *MergeConflictError
	ok := errors.As(err, &ce)
	if !ok {
		t.Fatalf("errors.As should succeed for *MergeConflictError, got %T: %v", err, err)
	}
	if ce == nil {
		t.Fatal("extracted *MergeConflictError should not be nil")
	}
}

// TestMergeTree_CommandArgsFormat verifies that MergeTree invokes git as
// "git merge-tree --write-tree <base> <head>" by inspecting GitError.Args
// on a known-failure path (invalid SHA arguments).
//
// TS-11-17
// Requirement: 11-REQ-5.3
func TestMergeTree_CommandArgsFormat(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	// Pass invalid SHAs to trigger a *GitError so we can inspect Args.
	_, err = runner.MergeTree(ctx, "invalid-base", "invalid-head")
	if err == nil {
		t.Fatal("expected non-nil error for invalid SHAs")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError for invalid SHAs, got %T: %v", err, err)
	}

	// Verify the command used merge-tree --write-tree.
	if len(ge.Args) < 2 {
		t.Fatalf("GitError.Args has %d elements, want >= 2: %v", len(ge.Args), ge.Args)
	}
	if ge.Args[0] != "merge-tree" {
		t.Errorf("GitError.Args[0] = %q, want %q", ge.Args[0], "merge-tree")
	}
	if ge.Args[1] != "--write-tree" {
		t.Errorf("GitError.Args[1] = %q, want %q", ge.Args[1], "--write-tree")
	}
}

// TestMergeTree_ConflictParsingDocumented verifies that the MergeTree
// implementation includes source code comments documenting the CONFLICT
// line parsing rule with a representative sample output line from git 2.38+.
//
// This is a static analysis check: it reads the package source files and
// asserts that a comment describing the parsing rule exists alongside a
// sample CONFLICT line such as:
//
//	CONFLICT (content): Merge conflict in path/to/file.go
//
// TS-11-18
// Requirement: 11-REQ-5.4
func TestMergeTree_ConflictParsingDocumented(t *testing.T) {
	// Read all non-test .go files in the package directory.
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	hasCONFLICTComment := false
	hasSampleLine := false
	for _, entry := range entries {
		if strings.HasSuffix(entry, "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(entry)
		if readErr != nil {
			t.Fatalf("read %s: %v", entry, readErr)
		}
		content := string(data)

		// Look for a comment that describes the CONFLICT parsing rule.
		// The comment should reference "CONFLICT" as part of the parsing logic.
		if strings.Contains(content, "CONFLICT") {
			hasCONFLICTComment = true
		}
		// Look for a representative sample line from git merge-tree output.
		if strings.Contains(content, "Merge conflict in") {
			hasSampleLine = true
		}
	}

	if !hasCONFLICTComment {
		t.Error("MergeTree implementation should contain a source code comment " +
			"referencing 'CONFLICT' to document the line parsing rule")
	}
	if !hasSampleLine {
		t.Error("MergeTree implementation should include a representative sample " +
			"CONFLICT line (e.g., 'CONFLICT (content): Merge conflict in path/to/file.go')")
	}
}
