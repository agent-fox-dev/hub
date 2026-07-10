// Package validate provides client-side validation for CLI flag values.
package validate

import "fmt"

// validExpires is the set of allowed --expires values.
var validExpires = map[int]bool{0: true, 30: true, 60: true, 90: true}

// ValidateExpires checks that the given expires value is one of {0, 30, 60, 90}.
// Returns an error with the message "Error: --expires must be one of: 0, 30, 60, 90"
// if the value is not in the allowed set.
func ValidateExpires(val int) error {
	if !validExpires[val] {
		return fmt.Errorf("Error: --expires must be one of: 0, 30, 60, 90")
	}
	return nil
}

// ValidateNonEmpty checks that the given flag value is a non-empty string.
// Returns a descriptive error naming the flag if the value is empty.
func ValidateNonEmpty(flag, val string) error {
	if val == "" {
		return fmt.Errorf("Error: --%s must not be empty", flag)
	}
	return nil
}
