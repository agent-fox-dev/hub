package cliconfig

import (
	"fmt"
	"os"
)

// WriteFunc is a function type for writing file content to a given path with
// specified permissions. Used for dependency injection in tests.
type WriteFunc func(path string, content []byte, perm os.FileMode) error

// defaultConfigContent is the initial content for a new config.toml file.
const defaultConfigContent = `# Client configuration for afc
# See docs/configuration.md for details.

hub_url = ""
`

// EnsureConfigExists creates the $HOME/.af/ directory and $HOME/.af/config.toml
// if they do not already exist. If both already exist, it returns nil without
// modifying either. New directories are created with 0700 and new files with
// 0600 permissions. Existing permissions are never changed.
func EnsureConfigExists(homeDir string) error {
	return EnsureConfigExistsWithWriter(homeDir, os.WriteFile)
}

// EnsureConfigExistsWithWriter is like EnsureConfigExists but accepts a custom
// writeFile function for testing write-failure scenarios.
func EnsureConfigExistsWithWriter(homeDir string, writeFile WriteFunc) error {
	configPath := ConfigFilePath(homeDir)

	// If the config file already exists, do nothing — preserve existing
	// content and permissions.
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}

	// Create the $HOME/.af/ directory if it does not exist.
	afDir := ConfigDir(homeDir)
	if err := os.MkdirAll(afDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", afDir, err)
	}

	// Write the initial config file.
	content := []byte(defaultConfigContent)
	if err := writeFile(configPath, content, 0600); err != nil {
		// Clean up any partial file left behind.
		os.Remove(configPath)
		return fmt.Errorf("failed to create config file %s: %w", configPath, err)
	}

	return nil
}
