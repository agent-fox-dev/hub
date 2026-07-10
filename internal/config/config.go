// Package config handles persistent CLI configuration stored at $HOME/.af/config.toml.
package config

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
	// Stub: not implemented yet.
	return nil
}

// EnsureConfigFile creates config.toml with mode 0600 and empty field values
// if it does not exist. Returns an error if the path exists but is not a
// regular file (e.g. a directory), or if the file contains unparseable TOML.
func EnsureConfigFile(path string) error {
	// Stub: not implemented yet.
	return nil
}

// Load reads and parses the config file at the given path. Returns an error
// wrapping the TOML parse error if the file contains invalid TOML.
func Load(path string) (*Config, error) {
	// Stub: returns empty config.
	return &Config{}, nil
}

// SaveFunc holds the function used to create temporary files. It can be
// replaced in tests to simulate os.CreateTemp failures.
var SaveCreateTemp = createTemp

// SaveRename holds the function used to atomically rename files. It can be
// replaced in tests to simulate os.Rename failures.
var SaveRename = rename

func createTemp(dir, pattern string) (TempFile, error) {
	// Stub: not implemented yet.
	return nil, nil
}

func rename(oldpath, newpath string) error {
	// Stub: not implemented yet.
	return nil
}

// TempFile is the interface satisfied by *os.File, used for test injection.
type TempFile interface {
	Name() string
	Write([]byte) (int, error)
	Close() error
}

// Save atomically writes the config to the given path using a temporary file
// and rename pattern.
func Save(path string, cfg *Config) error {
	// Stub: not implemented yet.
	return nil
}
