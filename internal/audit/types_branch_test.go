package audit

import "testing"

// TS-38-41: The audit package defines EventBranchPush as hub.branch.push.
// Requirement: 38-REQ-8.1
func TestEventBranchPushConstant_TS_38_41(t *testing.T) {
	if EventBranchPush != "hub.branch.push" {
		t.Errorf("EventBranchPush = %q; want %q", EventBranchPush, "hub.branch.push")
	}
}
