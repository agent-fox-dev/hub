package workspace

// validateSlug checks that slug is 3–64 characters, lowercase alphanumeric
// plus hyphens, starts with a letter, has no trailing hyphen, and has no
// consecutive hyphens.
func validateSlug(slug string) error {
	panic("not implemented")
}

// validateGitURL checks that git_url matches HTTPS format
// (https://<host>/<path>) or SSH format (git@<host>:<path>) where both host
// and path are non-empty. Validates by pattern matching only without DNS
// resolution or protocol negotiation.
func validateGitURL(url string) error {
	panic("not implemented")
}

// validateBranch checks that branch is non-empty and follows git ref naming
// rules: no ASCII control characters, no space, no ~, ^, :, ?, *, [, or \,
// no .. sequences, no trailing .lock, no trailing dot, no leading dot in any
// path component.
func validateBranch(branch string) error {
	panic("not implemented")
}
