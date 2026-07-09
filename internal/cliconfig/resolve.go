package cliconfig

import "fmt"

// ResolveHubURL returns the hub URL using the following precedence:
//  1. flagVal (--hub-url flag)
//  2. envVal (AF_HUB_URL environment variable)
//  3. cfg.HubURL from config file (empty string treated as unset)
//  4. Error mentioning config.toml, --hub-url, and AF_HUB_URL
func ResolveHubURL(flagVal, envVal string, cfg *Config) (string, error) {
	// Step 1: flag takes highest precedence.
	if flagVal != "" {
		return flagVal, nil
	}

	// Step 2: environment variable.
	if envVal != "" {
		return envVal, nil
	}

	// Step 3: config file value (empty string treated as unset).
	if cfg != nil && cfg.HubURL != "" {
		return cfg.HubURL, nil
	}

	// Step 4: nothing found — return descriptive error.
	return "", fmt.Errorf(
		"hub URL is required: use --hub-url flag, set AF_HUB_URL environment variable, or configure hub_url in $HOME/.af/config.toml",
	)
}

// ResolveAPIKey returns the API key token using the following precedence:
//  1. flagVal (--api-key flag value used directly as token)
//  2. envVal (AF_HUB_API_KEY environment variable used directly as token)
//  3. Config file: reads cfg.APIKey workspace_slug, retrieves token from
//     matching Keys[workspace_slug] entry (missing entry falls through)
//  4. Error mentioning config.toml, --api-key, and AF_HUB_API_KEY
func ResolveAPIKey(flagVal, envVal string, cfg *Config) (string, error) {
	// Step 1: flag takes highest precedence.
	if flagVal != "" {
		return flagVal, nil
	}

	// Step 2: environment variable.
	if envVal != "" {
		return envVal, nil
	}

	// Step 3: config file lookup via api_key workspace slug.
	if cfg != nil && cfg.APIKey != "" {
		// Look up the token from the matching keys entry.
		if cfg.Keys != nil {
			if entry, ok := cfg.Keys[cfg.APIKey]; ok && entry.Token != "" {
				return entry.Token, nil
			}
		}
		// api_key references a slug with no matching entry — fall through.
	}

	// Step 4: nothing found — return descriptive error.
	return "", fmt.Errorf(
		"API key is required: use --api-key flag, set AF_HUB_API_KEY environment variable, or configure api_key in $HOME/.af/config.toml",
	)
}
