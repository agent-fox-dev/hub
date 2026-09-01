package audit

import "testing"

// TS-17-60: DefaultSeverityFor returns the correct severity for each
// mapped event type.
func TestDefaultSeverityFor_KnownTypes(t *testing.T) {
	cases := []struct {
		eventType string
		want      string
	}{
		{"session.fail", "error"},
		{"run.limit_reached", "warning"},
		{"git.conflict", "warning"},
		{"harvest.empty", "warning"},
		{"review.parse_failure", "warning"},
	}
	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			got := DefaultSeverityFor(tc.eventType)
			if got != tc.want {
				t.Errorf("DefaultSeverityFor(%q) = %q, want %q",
					tc.eventType, got, tc.want)
			}
		})
	}
}

// TS-17-60 edge case 17-REQ-23.E1: Unknown event_type returns "info".
func TestDefaultSeverityFor_UnknownType(t *testing.T) {
	unknowns := []string{
		"session.start",
		"unknown.event",
		"custom.type",
		"",
		"session.success",
		"git.push",
	}
	for _, eventType := range unknowns {
		t.Run(eventType, func(t *testing.T) {
			got := DefaultSeverityFor(eventType)
			if got != "info" {
				t.Errorf("DefaultSeverityFor(%q) = %q, want %q",
					eventType, got, "info")
			}
		})
	}
}
