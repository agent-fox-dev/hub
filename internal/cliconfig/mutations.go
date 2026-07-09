package cliconfig

import "io"

// WriteLoginKey writes the login credentials to the config file.
// It adds or overwrites a [keys._login] section with the given keyID, token,
// and label="login". It sets api_key to "_login". If cfg.HubURL is currently
// empty, it sets hub_url to loginHubURL. Writes atomically via WriteConfigAtomic.
func WriteLoginKey(homeDir string, cfg *Config, keyID, token, loginHubURL string) error {
	// Stub: not yet implemented — will be done in task group 6.
	return nil
}

// AddKeyEntry adds a new key entry to the config file under
// [keys.<workspaceSlug>] with the given keyID, token, and label.
// Writes atomically via WriteConfigAtomic.
func AddKeyEntry(homeDir string, cfg *Config, workspaceSlug, keyID, token, label string) error {
	// Stub: not yet implemented — will be done in task group 6.
	return nil
}

// UpdateKeyToken finds the [keys.*] entry matching keyID and updates its
// token to newToken. If no matching entry is found, it writes a warning to
// stderr but returns nil error (the remote refresh may still have succeeded).
// Writes atomically via WriteConfigAtomic when a matching entry is found.
func UpdateKeyToken(homeDir string, cfg *Config, keyID, newToken string, stderr io.Writer) error {
	// Stub: not yet implemented — will be done in task group 6.
	return nil
}

// RemoveKeyEntry finds and removes the [keys.*] entry matching keyID from the
// config. If the removed entry's workspace slug matches cfg.APIKey, it clears
// api_key to empty string and writes a warning to stderr instructing the user
// to run 'afc keys default'. If no matching entry is found, it writes a
// warning to stderr but returns nil error.
// Writes atomically via WriteConfigAtomic when a matching entry is found.
func RemoveKeyEntry(homeDir string, cfg *Config, keyID string, stderr io.Writer) error {
	// Stub: not yet implemented — will be done in task group 6.
	return nil
}
