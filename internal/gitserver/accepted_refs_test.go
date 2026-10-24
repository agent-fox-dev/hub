package gitserver

import (
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
)

// A command is accepted when its report-status entry is ok or, without a
// report, when the pre-receive hook did not reject it; the branches to
// rebuild are the accepted refs/heads/ updates that are not deletes.
func TestAcceptedCommands(t *testing.T) {
	hash := func(c string) plumbing.Hash { return plumbing.NewHash(strings.Repeat(c, 40)) }
	cmds := []*packp.Command{
		{Name: "refs/heads/a", New: hash("a")},
		{Name: "refs/heads/b", New: hash("b")},
		{Name: "refs/tags/v1", New: hash("c")},
		{Name: "refs/heads/old", Old: hash("d"), New: plumbing.ZeroHash},
	}
	report := func(statuses map[string]string) *packp.ReportStatus {
		rs := &packp.ReportStatus{UnpackStatus: "ok"}
		for _, c := range cmds {
			rs.CommandStatuses = append(rs.CommandStatuses, &packp.CommandStatus{ReferenceName: c.Name, Status: statuses[string(c.Name)]})
		}
		return rs
	}
	for name, c := range map[string]struct {
		rs       *packp.ReportStatus
		rejected map[plumbing.ReferenceName]bool
		accepted []string
		branches []string
	}{
		"all ok":          {report(map[string]string{"refs/heads/a": "ok", "refs/heads/b": "ok", "refs/tags/v1": "ok", "refs/heads/old": "ok"}), nil, []string{"refs/heads/a", "refs/heads/b", "refs/tags/v1", "refs/heads/old"}, []string{"a", "b"}},
		"some failed":     {report(map[string]string{"refs/heads/a": "ok", "refs/heads/b": "blocked", "refs/tags/v1": "failed", "refs/heads/old": "ok"}), nil, []string{"refs/heads/a", "refs/heads/old"}, []string{"a"}},
		"none ok":         {report(nil), nil, nil, nil},
		"no report":       {nil, nil, []string{"refs/heads/a", "refs/heads/b", "refs/tags/v1", "refs/heads/old"}, []string{"a", "b"}},
		"no report, hook": {nil, map[plumbing.ReferenceName]bool{"refs/heads/b": true}, []string{"refs/heads/a", "refs/tags/v1", "refs/heads/old"}, []string{"a"}},
	} {
		accepted := acceptedCommands(cmds, c.rs, c.rejected)
		var names []string
		for _, cmd := range accepted {
			names = append(names, string(cmd.Name))
		}
		if !reflect.DeepEqual(names, c.accepted) || !reflect.DeepEqual(extractPushedBranches(accepted), c.branches) {
			t.Errorf("%s: accepted %v, branches %v; want %v, %v", name, names, extractPushedBranches(accepted), c.accepted, c.branches)
		}
	}
	if got := acceptedCommands(nil, report(map[string]string{"refs/heads/a": "ok"}), nil); got != nil {
		t.Errorf("no commands: %v; want none", got)
	}
}
