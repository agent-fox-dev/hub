// Package validate provides client-side validation for CLI flag values.
package validate

// ValidateExpires checks that the given expires value is one of {0, 30, 60, 90}.
// Returns an error with the message "Error: --expires must be one of: 0, 30, 60, 90"
// if the value is not in the allowed set.
func ValidateExpires(val int) error {
	// Stub: not implemented yet.
	return nil
}

// ValidateNonEmpty checks that the given flag value is a non-empty string.
// Returns a descriptive error naming the flag if the value is empty.
func ValidateNonEmpty(flag, val string) error {
	// Stub: not implemented yet.
	return nil
}
