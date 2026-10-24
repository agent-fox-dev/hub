package cli

import "testing"

func TestPatchStatus_ForwardsRequestAndPrintsDashboard(t *testing.T) {
	checkRoundTrips(t, []roundTrip{
		{"patch-status", []string{"workspace", "patch-status", "ws"}, "GET", "/workspaces/ws/patch-status", nil},
	})
}

func TestPatchStatus_RejectsWrongArgumentCount(t *testing.T) {
	checkArgCounts(t, [][]string{{"workspace", "patch-status"}, {"workspace", "patch-status", "ws", "extra"}})
}
