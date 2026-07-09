package cliconfig

import "os"

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
	// Stub: not yet implemented — will be done in task group 5.
	return nil
}

// WriteConfigAtomicWith is like WriteConfigAtomic but accepts custom
// createTemp and rename functions for dependency injection in tests.
func WriteConfigAtomicWith(homeDir string, cfg *Config, createTemp CreateTempFunc, rename RenameFunc) error {
	// Stub: not yet implemented — will be done in task group 5.
	return nil
}

// SetDefaultKey sets the api_key field in the config file to the given
// workspace slug, if a matching [keys.<slug>] section exists. Returns an
// error if the slug is not found. Writes atomically via WriteConfigAtomic.
func SetDefaultKey(homeDir string, cfg *Config, workspaceSlug string) error {
	// Stub: not yet implemented — will be done in task group 5/7.
	return nil
}
