package cliconfig

import "os"

// WriteFunc is a function type for writing file content to a given path with
// specified permissions. Used for dependency injection in tests.
type WriteFunc func(path string, content []byte, perm os.FileMode) error

// EnsureConfigExists creates the $HOME/.af/ directory and $HOME/.af/config.toml
// if they do not already exist. If both already exist, it returns nil without
// modifying either. New directories are created with 0700 and new files with
// 0600 permissions. Existing permissions are never changed.
func EnsureConfigExists(homeDir string) error {
	// Stub: not yet implemented — will be done in task group 5.
	return nil
}

// EnsureConfigExistsWithWriter is like EnsureConfigExists but accepts a custom
// writeFile function for testing write-failure scenarios.
func EnsureConfigExistsWithWriter(homeDir string, writeFile WriteFunc) error {
	// Stub: not yet implemented — will be done in task group 5.
	return nil
}
