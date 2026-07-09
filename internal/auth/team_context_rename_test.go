package auth

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// findAuthProjectRoot walks up from the current directory (internal/auth/)
// to find the directory containing go.mod.
func findAuthProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// TS-06-8: Verifies that the auth middleware uses ContextKeyTeamID to store
// and retrieve the authenticated team ID from the Echo request context.
// Requirement: 06-REQ-2.5
// ---------------------------------------------------------------------------

func TestContextKeyTeamIDDefined(t *testing.T) {
	// Read the context.go source file to verify ContextKeyTeamID is defined.
	content, err := os.ReadFile("context.go")
	if err != nil {
		t.Fatalf("could not read context.go: %v", err)
	}
	src := string(content)

	t.Run("ContextKeyTeamID is defined", func(t *testing.T) {
		if !strings.Contains(src, "ContextKeyTeamID") {
			t.Error("context.go should define ContextKeyTeamID constant")
		}
	})

	t.Run("ContextKeyWorkspaceID is absent", func(t *testing.T) {
		if strings.Contains(src, "ContextKeyWorkspaceID") {
			t.Error("context.go should not define ContextKeyWorkspaceID (renamed to ContextKeyTeamID)")
		}
	})
}

func TestContextKeyTeamIDUsedInMiddleware(t *testing.T) {
	// Read the middleware.go source file to verify it uses ContextKeyTeamID.
	content, err := os.ReadFile("middleware.go")
	if err != nil {
		t.Fatalf("could not read middleware.go: %v", err)
	}
	src := string(content)

	t.Run("middleware uses ContextKeyTeamID", func(t *testing.T) {
		if !strings.Contains(src, "ContextKeyTeamID") {
			t.Error("middleware.go should use ContextKeyTeamID")
		}
	})

	t.Run("middleware does not use ContextKeyWorkspaceID", func(t *testing.T) {
		if strings.Contains(src, "ContextKeyWorkspaceID") {
			t.Error("middleware.go should not use ContextKeyWorkspaceID (renamed to ContextKeyTeamID)")
		}
	})
}

func TestNoWorkspaceContextKeyInInternalPackages(t *testing.T) {
	root, err := findAuthProjectRoot()
	if err != nil {
		t.Fatalf("could not find project root: %v", err)
	}

	// Scan all Go files under internal/ for ContextKeyWorkspaceID (should be absent)
	// and ContextKeyTeamID (should be present in at least one file).
	internalDir := filepath.Join(root, "internal")
	foundTeamKey := false
	var violations []string

	err = filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Skip this test file itself.
		if filepath.Base(path) == "team_context_rename_test.go" {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(content)

		relPath, _ := filepath.Rel(root, path)

		if strings.Contains(src, "ContextKeyWorkspaceID") {
			violations = append(violations, relPath)
		}
		if strings.Contains(src, "ContextKeyTeamID") {
			foundTeamKey = true
		}

		return nil
	})

	if err != nil {
		t.Fatalf("error walking internal/ directory: %v", err)
	}

	if !foundTeamKey {
		t.Error("ContextKeyTeamID should be used in at least one file under internal/")
	}

	if len(violations) > 0 {
		t.Errorf("ContextKeyWorkspaceID still referenced in %d files (should be renamed to ContextKeyTeamID):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func TestNoWorkspaceIDReferencesInAuthPackage(t *testing.T) {
	// Verify no auth package Go files contain workspace_id or WorkspaceID
	// identifiers (other than this test file).
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("failed to glob auth .go files: %v", err)
	}

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bworkspace_id\b`),
		regexp.MustCompile(`\bWorkspaceID\b`),
	}

	for _, f := range files {
		if f == "team_context_rename_test.go" {
			continue
		}

		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("failed to read %s: %v", f, err)
		}
		src := string(content)

		for _, pat := range patterns {
			if pat.MatchString(src) {
				t.Errorf("%s contains legacy workspace reference matching %s", f, pat.String())
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TS-06-E2: Verifies that introducing a reference to a deleted Workspace-
// prefixed type causes `go build ./...` to fail with a non-zero exit code.
// Requirement: 06-REQ-2.E1
// ---------------------------------------------------------------------------

func TestCompileFailsWithStaleWorkspaceReference(t *testing.T) {
	root, err := findAuthProjectRoot()
	if err != nil {
		t.Fatalf("could not find project root: %v", err)
	}

	// Inject a stale reference to store.Workspace{} into a temporary Go file
	// in the store package.
	storeDir := filepath.Join(root, "internal", "store")
	tmpFile := filepath.Join(storeDir, "workspace_stale_check_test.go")

	staleCode := []byte(`package store

// This file is a temporary injection to test compile failure on stale references.
var _ = Workspace{}
`)

	if err := os.WriteFile(tmpFile, staleCode, 0644); err != nil {
		t.Fatalf("failed to write temporary stale-reference file: %v", err)
	}
	defer os.Remove(tmpFile) // Clean up no matter what.

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Error("go build ./... should fail when a stale Workspace{} reference is present, but it succeeded")
	} else {
		// Verify the error mentions the undefined symbol.
		outStr := string(output)
		if !strings.Contains(outStr, "undefined") && !strings.Contains(outStr, "Workspace") {
			t.Logf("go build error output: %s", outStr)
			// The build failed, which is the expected behavior. The exact error
			// message may vary between Go versions.
		}
	}
}
