package gitserver

import (
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
)

// acceptedCommands returns the subset of commands that were accepted by the
// receive-pack session. A command is accepted when:
//   - A ReportStatus is available and the command's CommandStatuses entry
//     (matched by ReferenceName) has Status "ok".
//   - No ReportStatus is available and the pre-receive hook did not reject
//     the command (i.e. the ref name is not in hookRejected).
func acceptedCommands(commands []*packp.Command, rs *packp.ReportStatus, hookRejected map[plumbing.ReferenceName]bool) []*packp.Command {
	if len(commands) == 0 {
		return nil
	}

	var accepted []*packp.Command

	if rs != nil {
		// Build a lookup from the report status.
		okRefs := make(map[plumbing.ReferenceName]bool, len(rs.CommandStatuses))
		for _, cs := range rs.CommandStatuses {
			if cs.Status == "ok" {
				okRefs[cs.ReferenceName] = true
			}
		}
		for _, cmd := range commands {
			if okRefs[cmd.Name] {
				accepted = append(accepted, cmd)
			}
		}
	} else {
		// No report status: accept everything the hook did not reject.
		for _, cmd := range commands {
			if !hookRejected[cmd.Name] {
				accepted = append(accepted, cmd)
			}
		}
	}

	return accepted
}
