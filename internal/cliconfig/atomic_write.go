package cliconfig

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// CreateTempFunc is the type for os.CreateTemp — used for dependency injection
// in tests to simulate CreateTemp failures.
type CreateTempFunc func(dir, pattern string) (*os.File, error)

// RenameFunc is the type for os.Rename — used for dependency injection in
// tests to simulate Rename failures.
type RenameFunc func(oldpath, newpath string) error

// WriteConfigAtomic writes the given Config to $HOME/.af/config.toml
// atomically: it encodes the config to a temp file created in $HOME/.af/
// with 0600 permissions, then renames it over the target file. If the
// rename fails, the temp file is cleaned up via deferred removal.
func WriteConfigAtomic(homeDir string, cfg *Config) error {
	return WriteConfigAtomicWith(homeDir, cfg, os.CreateTemp, os.Rename)
}

// WriteConfigAtomicWith is like WriteConfigAtomic but accepts custom
// createTemp and rename functions for dependency injection in tests.
func WriteConfigAtomicWith(homeDir string, cfg *Config, createTemp CreateTempFunc, rename RenameFunc) error {
	afDir := ConfigDir(homeDir)
	configPath := ConfigFilePath(homeDir)

	// Create a temp file in the same directory for atomic rename.
	tmpFile, err := createTemp(afDir, "config-*.toml")
	if err != nil {
		return fmt.Errorf("failed to create temp file for config write: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Deferred cleanup: remove the temp file if we don't successfully rename it.
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Set permissions to 0600 before writing content.
	if err := tmpFile.Chmod(0600); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to set temp file permissions: %w", err)
	}

	// Encode the config as TOML to the temp file.
	encoder := toml.NewEncoder(tmpFile)
	if err := encoder.Encode(cfg); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to encode config to TOML: %w", err)
	}

	// Close the temp file before renaming.
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomically replace the config file.
	if err := rename(tmpPath, configPath); err != nil {
		return fmt.Errorf("failed to rename temp file to config file: %w", err)
	}

	success = true
	return nil
}

// SetDefaultKey sets the api_key field in the config file to the given
// workspace slug, if a matching [keys.<slug>] section exists. Returns an
// error if the slug is not found. Writes atomically via WriteConfigAtomic.
func SetDefaultKey(homeDir string, cfg *Config, workspaceSlug string) error {
	// Verify the workspace slug has a matching keys entry.
	if _, ok := cfg.Keys[workspaceSlug]; !ok {
		return fmt.Errorf("no key entry found for workspace %q in config; available keys: use 'afc keys create' first", workspaceSlug)
	}

	cfg.APIKey = workspaceSlug
	return WriteConfigAtomic(homeDir, cfg)
}
