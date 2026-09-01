package audit

// DefaultSeverityFor returns the default severity for the given event type.
// Returns "error" for session.fail, "warning" for run.limit_reached,
// git.conflict, harvest.empty, and review.parse_failure, and "info"
// for all other event types.
func DefaultSeverityFor(eventType string) string {
	panic("not implemented")
}
