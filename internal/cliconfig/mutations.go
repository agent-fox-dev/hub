package cliconfig

import (
	"fmt"
	"io"
)

// WriteLoginKey writes the login credentials to the config file.
// It adds or overwrites a [keys._login] section with the given keyID, token,
// and label="login". It sets api_key to "_login". If cfg.HubURL is currently
// empty, it sets hub_url to loginHubURL. Writes atomically via WriteConfigAtomic.
//
// If keyID or token is empty, returns an error without modifying the config
// (defense against malformed server responses).
func WriteLoginKey(homeDir string, cfg *Config, keyID, token, loginHubURL string) error {
	// Validate that we have actual credentials to store.
	if keyID == "" || token == "" {
		return fmt.Errorf("login response missing credential data: key_id and token are required")
	}

	// Initialize Keys map if nil.
	if cfg.Keys == nil {
		cfg.Keys = make(map[string]KeyEntry)
	}

	// Add or overwrite the _login key entry.
	cfg.Keys["_login"] = KeyEntry{
		KeyID: keyID,
		Token: token,
		Label: "login",
	}

	// Set api_key to _login.
	cfg.APIKey = "_login"

	// Only set hub_url if it is currently empty.
	if cfg.HubURL == "" {
		cfg.HubURL = loginHubURL
	}

	return WriteConfigAtomic(homeDir, cfg)
}

// AddKeyEntry adds a new key entry to the config file under
// [keys.<workspaceSlug>] with the given keyID, token, and label.
// Writes atomically via WriteConfigAtomic.
func AddKeyEntry(homeDir string, cfg *Config, workspaceSlug, keyID, token, label string) error {
	// Initialize Keys map if nil.
	if cfg.Keys == nil {
		cfg.Keys = make(map[string]KeyEntry)
	}

	cfg.Keys[workspaceSlug] = KeyEntry{
		KeyID: keyID,
		Token: token,
		Label: label,
	}

	return WriteConfigAtomic(homeDir, cfg)
}

// UpdateKeyToken finds the [keys.*] entry matching keyID and updates its
// token to newToken. If no matching entry is found, it writes a warning to
// stderr but returns nil error (the remote refresh may still have succeeded).
// Writes atomically via WriteConfigAtomic when a matching entry is found.
func UpdateKeyToken(homeDir string, cfg *Config, keyID, newToken string, stderr io.Writer) error {
	// Search for the entry with the matching key_id.
	for slug, entry := range cfg.Keys {
		if entry.KeyID == keyID {
			entry.Token = newToken
			cfg.Keys[slug] = entry
			return WriteConfigAtomic(homeDir, cfg)
		}
	}

	// Key not found in config — warn but don't error.
	fmt.Fprintf(stderr, "Warning: key %s not found in local config, skipping config update\n", keyID)
	return nil
}

// RemoveKeyEntry finds and removes the [keys.*] entry matching keyID from the
// config. If the removed entry's workspace slug matches cfg.APIKey, it clears
// api_key to empty string and writes a warning to stderr instructing the user
// to run 'afc keys default'. If no matching entry is found, it writes a
// warning to stderr but returns nil error.
// Writes atomically via WriteConfigAtomic when a matching entry is found.
func RemoveKeyEntry(homeDir string, cfg *Config, keyID string, stderr io.Writer) error {
	// Search for the entry with the matching key_id.
	var foundSlug string
	for slug, entry := range cfg.Keys {
		if entry.KeyID == keyID {
			foundSlug = slug
			break
		}
	}

	if foundSlug == "" {
		// Key not found in config — warn but don't error.
		fmt.Fprintf(stderr, "Warning: key %s not found in local config, skipping config update\n", keyID)
		return nil
	}

	// Remove the key entry.
	delete(cfg.Keys, foundSlug)

	// If the removed key was the default, clear api_key and warn.
	if cfg.APIKey == foundSlug {
		cfg.APIKey = ""
		fmt.Fprintf(stderr, "Warning: default key removed; run afc keys default <workspace-slug> to set a new default\n")
	}

	return WriteConfigAtomic(homeDir, cfg)
}
