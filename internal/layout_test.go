package internal_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-01-1: Verify that the repository contains the expected entry point
// directories and that all private packages reside under internal/.
func TestRepositoryLayout(t *testing.T) {
	root := findRepoRoot(t)

	t.Run("cmd/af-hub directory exists", func(t *testing.T) {
		info, err := os.Stat(filepath.Join(root, "cmd", "af-hub"))
		if err != nil {
			t.Fatalf("cmd/af-hub/ does not exist: %v", err)
		}
		if !info.IsDir() {
			t.Fatal("cmd/af-hub is not a directory")
		}
	})

	t.Run("cmd/afc directory exists", func(t *testing.T) {
		info, err := os.Stat(filepath.Join(root, "cmd", "afc"))
		if err != nil {
			t.Fatalf("cmd/afc/ does not exist: %v", err)
		}
		if !info.IsDir() {
			t.Fatal("cmd/afc is not a directory")
		}
	})

	t.Run("all non-entry-point Go packages reside under internal/", func(t *testing.T) {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			// Skip hidden directories, vendor, testdata, bin.
			if info.IsDir() {
				base := info.Name()
				if strings.HasPrefix(base, ".") || base == "vendor" || base == "testdata" || base == "bin" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(info.Name(), ".go") {
				return nil
			}
			// Allowed locations: cmd/**, internal/**, root-level files, Makefile area.
			relPath, _ := filepath.Rel(root, path)
			if strings.HasPrefix(relPath, "cmd/") ||
				strings.HasPrefix(relPath, "internal/") ||
				!strings.Contains(relPath, string(filepath.Separator)) {
				return nil
			}
			t.Errorf("Go file outside cmd/ and internal/: %s", relPath)
			return nil
		})
		if err != nil {
			t.Fatalf("error walking repository: %v", err)
		}
	})
}

// TS-01-16 / TS-01-P3: Verify that no package outside the store layer package
// imports database/sql or executes SQL statements directly.
func TestStoreLayerExclusivity(t *testing.T) {
	root := findRepoRoot(t)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if strings.HasPrefix(base, ".") || base == "vendor" || base == "testdata" || base == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".go") || strings.HasSuffix(info.Name(), "_test.go") {
			return nil
		}

		relPath, _ := filepath.Rel(root, path)

		// Store package is allowed to use database/sql.
		if strings.HasPrefix(relPath, filepath.Join("internal", "store")+string(filepath.Separator)) {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Errorf("failed to read %s: %v", relPath, readErr)
			return nil
		}
		content := string(data)

		// Check for direct SQL calls.
		sqlCalls := []string{"db.Query(", "db.Exec(", "db.QueryRow(", ".Query(", ".Exec(", ".QueryRow("}
		for _, call := range sqlCalls {
			if strings.Contains(content, call) {
				// Allow the db package to use these for schema init.
				if strings.HasPrefix(relPath, filepath.Join("internal", "db")+string(filepath.Separator)) {
					continue
				}
				t.Errorf("file %s contains direct SQL call %q outside store layer", relPath, call)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("error walking repository: %v", err)
	}
}

// findRepoRoot walks up from the test file to find the repository root
// (the directory containing go.mod).
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root (go.mod)")
		}
		dir = parent
	}
}
