package audit

import "testing"

// TS-27-59: The audit event constants of the sandbox gateway have the expected
// names.
// Requirement: 27-REQ-10.1
func TestSandboxGatewayEventConstants_TS_27_59(t *testing.T) {
	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"EventSandboxCreate", EventSandboxCreate, "hub.sandbox.create"},
		{"EventSandboxFailed", EventSandboxFailed, "hub.sandbox.failed"},
		{"EventSandboxDestroy", EventSandboxDestroy, "hub.sandbox.destroy"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q; want %q", c.name, c.got, c.want)
		}
	}
}
