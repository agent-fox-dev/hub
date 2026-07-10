package teams

import (
	"net/url"
	"regexp"
)

// slugRegex enforces the slug format: 3-64 lowercase alphanumeric characters
// and hyphens, starting with a letter, ending with a letter or digit.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}[a-z0-9]$`)

// ValidateName checks that the team name (already trimmed) is non-empty
// and does not exceed 255 characters.
func ValidateName(name string) error {
	if name == "" || len(name) > 255 {
		return ErrInvalidTeamName
	}
	return nil
}

// ValidateSlug checks that the slug matches the required format:
// ^[a-z][a-z0-9-]{1,62}[a-z0-9]$ (3-64 chars, starts with a letter,
// ends with a letter or digit, lowercase alphanumeric and hyphens only).
func ValidateSlug(slug string) error {
	if !slugRegex.MatchString(slug) {
		return ErrInvalidSlugFormat
	}
	return nil
}

// ValidateURL checks that the URL has an http or https scheme and a host.
// Empty URLs should not be passed to this function; the caller is responsible
// for treating absent URLs as valid (nullable field).
func ValidateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ErrInvalidURLFormat
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrInvalidURLFormat
	}
	if u.Host == "" {
		return ErrInvalidURLFormat
	}
	return nil
}
