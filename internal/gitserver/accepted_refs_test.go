package gitserver

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
)

// TS-22-29: A ref is accepted when its report-status entry is ok, or,
// with no report, unless the hook rejected it.
// Verifies: 22-REQ-5.1
func TestAcceptedCommands_TS22_29_ClassifiesAccepted(t *testing.T) {
	cmdA := &packp.Command{
		Name: plumbing.ReferenceName("refs/heads/a"),
		Old:  plumbing.ZeroHash,
		New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	}
	cmdB := &packp.Command{
		Name: plumbing.ReferenceName("refs/heads/b"),
		Old:  plumbing.ZeroHash,
		New:  plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	}
	cmdC := &packp.Command{
		Name: plumbing.ReferenceName("refs/heads/c"),
		Old:  plumbing.ZeroHash,
		New:  plumbing.NewHash("cccccccccccccccccccccccccccccccccccccccc"),
	}

	allCmds := []*packp.Command{cmdA, cmdB, cmdC}

	// --- Sub-test 1: With a ReportStatus, only "ok" entries are accepted ---
	t.Run("with_report_status", func(t *testing.T) {
		rs := &packp.ReportStatus{
			UnpackStatus: "ok",
			CommandStatuses: []*packp.CommandStatus{
				{ReferenceName: plumbing.ReferenceName("refs/heads/a"), Status: "ok"},
				{ReferenceName: plumbing.ReferenceName("refs/heads/b"), Status: "some error"},
				{ReferenceName: plumbing.ReferenceName("refs/heads/c"), Status: "ok"},
			},
		}

		accepted := acceptedCommands(allCmds, rs, nil)
		names := cmdNames(accepted)

		if len(names) != 2 {
			t.Fatalf("expected 2 accepted commands, got %d: %v", len(names), names)
		}
		if !containsName(names, "refs/heads/a") {
			t.Error("expected refs/heads/a to be accepted")
		}
		if containsName(names, "refs/heads/b") {
			t.Error("expected refs/heads/b to NOT be accepted")
		}
		if !containsName(names, "refs/heads/c") {
			t.Error("expected refs/heads/c to be accepted")
		}
	})

	// --- Sub-test 2: Without a report, accepted unless hook-rejected ---
	t.Run("without_report_hook_rejected", func(t *testing.T) {
		hookRejected := map[plumbing.ReferenceName]bool{
			plumbing.ReferenceName("refs/heads/c"): true,
		}

		accepted := acceptedCommands(allCmds, nil, hookRejected)
		names := cmdNames(accepted)

		if len(names) != 2 {
			t.Fatalf("expected 2 accepted commands, got %d: %v", len(names), names)
		}
		if !containsName(names, "refs/heads/a") {
			t.Error("expected refs/heads/a to be accepted")
		}
		if !containsName(names, "refs/heads/b") {
			t.Error("expected refs/heads/b to be accepted")
		}
		if containsName(names, "refs/heads/c") {
			t.Error("expected refs/heads/c to NOT be accepted")
		}
	})

	// --- Sub-test 3: Without report and no hook rejections, all accepted ---
	t.Run("without_report_no_rejections", func(t *testing.T) {
		accepted := acceptedCommands(allCmds, nil, nil)
		names := cmdNames(accepted)

		if len(names) != 3 {
			t.Fatalf("expected 3 accepted commands, got %d: %v", len(names), names)
		}
	})

	// --- Sub-test 4: Report takes precedence over hookRejected ---
	t.Run("report_takes_precedence", func(t *testing.T) {
		rs := &packp.ReportStatus{
			UnpackStatus: "ok",
			CommandStatuses: []*packp.CommandStatus{
				{ReferenceName: plumbing.ReferenceName("refs/heads/a"), Status: "ok"},
				{ReferenceName: plumbing.ReferenceName("refs/heads/b"), Status: "error"},
				{ReferenceName: plumbing.ReferenceName("refs/heads/c"), Status: "ok"},
			},
		}
		hookRejected := map[plumbing.ReferenceName]bool{
			plumbing.ReferenceName("refs/heads/a"): true,
		}

		// When report is available, hookRejected is ignored; report is the source of truth.
		accepted := acceptedCommands(allCmds, rs, hookRejected)
		names := cmdNames(accepted)

		if len(names) != 2 {
			t.Fatalf("expected 2 accepted commands, got %d: %v", len(names), names)
		}
		// a is ok in report, so accepted despite being in hookRejected
		if !containsName(names, "refs/heads/a") {
			t.Error("expected refs/heads/a to be accepted (report says ok)")
		}
	})

	// --- Sub-test 5: Command not in report is not accepted ---
	t.Run("command_missing_from_report", func(t *testing.T) {
		rs := &packp.ReportStatus{
			UnpackStatus: "ok",
			CommandStatuses: []*packp.CommandStatus{
				{ReferenceName: plumbing.ReferenceName("refs/heads/a"), Status: "ok"},
				// b and c not in report
			},
		}

		accepted := acceptedCommands(allCmds, rs, nil)
		names := cmdNames(accepted)

		if len(names) != 1 {
			t.Fatalf("expected 1 accepted command, got %d: %v", len(names), names)
		}
		if !containsName(names, "refs/heads/a") {
			t.Error("expected refs/heads/a to be accepted")
		}
	})
}

// cmdNames extracts ref names from a slice of commands.
func cmdNames(cmds []*packp.Command) []string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = string(c.Name)
	}
	return names
}

// containsName checks if a name is in the list.
func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
