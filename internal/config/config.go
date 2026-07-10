// Package config handles persistent CLI configuration stored at $HOME/.af/config.toml.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config represents the persistent CLI configuration stored in config.toml.
type Config struct {
	HubURL string `toml:"hub_url"`
	UserID string `toml:"user_id"`
	APIKey string `toml:"api_key"`
	KeyID  string `toml:"key_id"`
}

// ConfigDir returns the path to the .af configuration directory under the given home.
func ConfigDir(home string) string {
	return home + "/.af"
}

// ConfigPath returns the path to config.toml under the given home.
func ConfigPath(home string) string {
	return home + "/.af/config.toml"
}

// EnsureConfigDir creates $HOME/.af/ with mode 0700 if it does not exist.
func EnsureConfigDir(home string) error {
	dir := ConfigDir(home)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}
	return nil
}

// EnsureConfigFile creates config.toml with mode 0600 and empty field values
// if it does not exist. Returns an error if the path exists but is not a
// regular file (e.g. a directory).
func EnsureConfigFile(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		// Path exists — check if it's a directory.
		if info.IsDir() {
			return fmt.Errorf("config path %s is a directory, not a file", path)
		}
		// File exists and is a regular file — nothing to create.
		return nil
	}
	if !os.IsNotExist(err) {
		// Unexpected stat error (e.g. permission denied).
		return fmt.Errorf("failed to check config file %s: %w", path, err)
	}

	// File does not exist — create it with empty config values.
	return Save(path, &Config{})
}

// Load reads and parses the config file at the given path. Returns an error
// wrapping the TOML parse error if the file contains invalid TOML.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}

// SaveCreateTemp holds the function used to create temporary files. It can be
// replaced in tests to simulate os.CreateTemp failures.
var SaveCreateTemp = createTemp

// SaveRename holds the function used to atomically rename files. It can be
// replaced in tests to simulate os.Rename failures.
var SaveRename = rename

func createTemp(dir, pattern string) (TempFile, error) {
	return os.CreateTemp(dir, pattern)
}

func rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// TempFile is the interface satisfied by *os.File, used for test injection.
type TempFile interface {
	Name() string
	Write([]byte) (int, error)
	Close() error
}

// Save atomically writes the config to the given path using a temporary file
// and rename pattern. The temporary file is created in the same directory as
// the target with prefix "config.toml." and mode 0600.
func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)

	// Step 1: Create temporary file in the same directory.
	tmpFile, err := SaveCreateTemp(dir, "config.toml.")
	if err != nil {
		return fmt.Errorf("failed to create temp config file: %w", err)
	}
	tmpName := tmpFile.Name()

	// Step 2: Encode config as TOML into the temp file.
	if err := toml.NewEncoder(tmpFile).Encode(cfg); err != nil {
		tmpFile.Close()
		os.Remove(tmpName) // best-effort cleanup
		return fmt.Errorf("failed to encode config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName) // best-effort cleanup
		return fmt.Errorf("failed to close temp config file: %w", err)
	}

	// Step 3: Set permissions to 0600 before rename.
	if err := os.Chmod(tmpName, 0600); err != nil {
		os.Remove(tmpName) // best-effort cleanup
		return fmt.Errorf("failed to set config file permissions: %w", err)
	}

	// Step 4: Atomic rename. On failure, the temp file may remain for
	// manual cleanup (per spec 05-REQ-3.E2).
	if err := SaveRename(tmpName, path); err != nil {
		return fmt.Errorf("failed to rename temp config file: %w", err)
	}

	return nil
}
