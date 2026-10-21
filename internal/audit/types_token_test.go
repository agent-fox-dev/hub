package audit

import "testing"

// TS-34-51: The audit constants have the specified values.
// Requirement: 34-REQ-9.1
func TestTokenEventConstants_TS_34_51(t *testing.T) {
	if EventTokenBind != "hub.token.bind" {
		t.Errorf("EventTokenBind = %q; want hub.token.bind", EventTokenBind)
	}
	if EventTokenBoundRefused != "hub.token.bound_refused" {
		t.Errorf("EventTokenBoundRefused = %q; want hub.token.bound_refused", EventTokenBoundRefused)
	}
}
