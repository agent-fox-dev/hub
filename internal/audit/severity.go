package audit

// defaultSeverityMap maps specific event types to their default severity.
var defaultSeverityMap = map[string]string{
	"session.fail":         "error",
	"run.limit_reached":    "warning",
	"git.conflict":         "warning",
	"harvest.empty":        "warning",
	"review.parse_failure": "warning",
}

// DefaultSeverityFor returns the default severity for the given event type.
// Returns "error" for session.fail, "warning" for run.limit_reached,
// git.conflict, harvest.empty, and review.parse_failure, and "info"
// for all other event types.
func DefaultSeverityFor(eventType string) string {
	if s, ok := defaultSeverityMap[eventType]; ok {
		return s
	}
	return "info"
}
