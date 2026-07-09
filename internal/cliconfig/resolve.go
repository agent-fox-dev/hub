package cliconfig

// ResolveHubURL returns the hub URL using the following precedence:
//  1. flagVal (--hub-url flag)
//  2. envVal (AF_HUB_URL environment variable)
//  3. cfg.HubURL from config file (empty string treated as unset)
//  4. Error mentioning config.toml, --hub-url, and AF_HUB_URL
func ResolveHubURL(flagVal, envVal string, cfg *Config) (string, error) {
	// Stub: not yet implemented — will be done in task group 5.
	return "", nil
}

// ResolveAPIKey returns the API key token using the following precedence:
//  1. flagVal (--api-key flag value used directly as token)
//  2. envVal (AF_HUB_API_KEY environment variable used directly as token)
//  3. Config file: reads cfg.APIKey workspace_slug, retrieves token from
//     matching Keys[workspace_slug] entry (missing entry falls through)
//  4. Error mentioning config.toml, --api-key, and AF_HUB_API_KEY
func ResolveAPIKey(flagVal, envVal string, cfg *Config) (string, error) {
	// Stub: not yet implemented — will be done in task group 5.
	return "", nil
}
