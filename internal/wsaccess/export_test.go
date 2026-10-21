package wsaccess

// ResetAllowlistForTest empties the bound-token allowlist so that a test sees
// only the routes it marks itself.
func ResetAllowlistForTest() {
	boundTokenAllowlist.Lock()
	defer boundTokenAllowlist.Unlock()
	boundTokenAllowlist.routes = make(map[string]bool)
}
