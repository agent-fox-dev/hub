// Package validator provides input validation functions for workspace entities.
package validator

import (
	"regexp"
	"strings"
)

// slugRegexp validates workspace slugs: starts with a lowercase letter,
// followed by lowercase alphanumeric characters or hyphens, and ends with
// a lowercase alphanumeric character. Total length 3–64 characters.
//
// Note: consecutive hyphens ("--") are rejected by an additional string check
// because a single regex cannot cleanly enforce both the character set and
// the no-consecutive-hyphen rule.
var slugRegexp = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}[a-z0-9]$`)

// ValidateSlug checks whether the given slug conforms to the required format:
// lowercase alphanumeric and hyphens, 3-64 chars, starts with a letter,
// does not end with a hyphen, no consecutive hyphens.
func ValidateSlug(slug string) bool {
	if len(slug) < 3 || len(slug) > 64 {
		return false
	}
	if strings.Contains(slug, "--") {
		return false
	}
	return slugRegexp.MatchString(slug)
}
