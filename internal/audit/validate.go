package audit

import (
	"fmt"
	"regexp"
)

// runIDRegexp validates run_id format: YYYYMMDD_HHMMSS_6hexchars.
// Only lowercase hex characters are accepted in the suffix.
var runIDRegexp = regexp.MustCompile(`^\d{8}_\d{6}_[0-9a-f]{6}$`)

// uuidFormatRegexp validates standard UUID format: 8-4-4-4-12 hex digits with dashes.
var uuidFormatRegexp = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validSeverities is the set of accepted severity values.
var validSeverities = map[string]bool{
	"info":     true,
	"warning":  true,
	"error":    true,
	"critical": true,
}

// validTraceEventTypes is the closed set of accepted trace event types.
var validTraceEventTypes = map[string]bool{
	"session.init":      true,
	"assistant.message": true,
	"tool.use":          true,
	"tool.error":        true,
	"session.result":    true,
}

// ValidateRunID checks whether runID matches the required format:
// YYYYMMDD_HHMMSS_6hexchars (e.g. 20260704_143022_a1b2c3).
// Only lowercase hex characters are accepted.
func ValidateRunID(runID string) bool {
	return runIDRegexp.MatchString(runID)
}

// ValidateRunIDErr returns an error if runID does not match the required format.
func ValidateRunIDErr(runID string) error {
	if !ValidateRunID(runID) {
		return fmt.Errorf("invalid run_id format: %q (expected YYYYMMDD_HHMMSS_6hexchars)", runID)
	}
	return nil
}

// ValidateTraceEventType returns an error if et is not one of the five valid
// trace event types: session.init, assistant.message, tool.use, tool.error,
// session.result.
func ValidateTraceEventType(et string) error {
	if !validTraceEventTypes[et] {
		return fmt.Errorf("unknown trace event_type: %q", et)
	}
	return nil
}

// ValidateUUID returns an error if id is not a valid UUID format
// (8-4-4-4-12 hex digits with dashes).
func ValidateUUID(id string) error {
	if !uuidFormatRegexp.MatchString(id) {
		return fmt.Errorf("invalid UUID format: %q", id)
	}
	return nil
}

// ValidateSeverity returns an error if s is not one of the four valid
// severity values: info, warning, error, critical.
func ValidateSeverity(s string) error {
	if !validSeverities[s] {
		return fmt.Errorf("invalid severity: %q (must be one of: info, warning, error, critical)", s)
	}
	return nil
}
