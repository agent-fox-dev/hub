package forge

import (
	"net/http"
	"net/url"
)

// target selects which of the workspace's remotes a route reads.
type target string

const (
	// targetOrigin is the workspace's git_url, and the default.
	targetOrigin target = "origin"
	// targetUpstream is the upstream_url of a carry_patch workspace.
	targetUpstream target = "upstream"
)

// workspaceRemotes is what the forge routes read of a workspace's row.
type workspaceRemotes struct {
	gitURL      string
	mode        string
	upstreamURL string
}

// carryPatchMode is the workspace_mode of a fork that hub syncs from an upstream.
const carryPatchMode = "carry_patch"

// hasUpstream reports whether the workspace is a carry_patch fork with an upstream
// to read.
func (w workspaceRemotes) hasUpstream() bool {
	return w.mode == carryPatchMode && w.upstreamURL != ""
}

// refusal is a request a route refuses before it contacts the forge.
type refusal struct {
	status    int
	message   string
	errorType string
}

// resolveTarget reads ?target= of a request. Absent and origin select the git_url.
// upstream is allowed only on a carry_patch workspace with an upstream_url; any
// other value, an empty one and a repeated parameter included, is refused with the
// allowed values.
func resolveTarget(q url.Values, ws workspaceRemotes) (target, *refusal) {
	values, present := q["target"]
	if !present {
		return targetOrigin, nil
	}
	if len(values) == 1 {
		switch target(values[0]) {
		case targetOrigin:
			return targetOrigin, nil
		case targetUpstream:
			if ws.hasUpstream() {
				return targetUpstream, nil
			}
			return "", &refusal{
				status:    http.StatusBadRequest,
				message:   "target=upstream needs a carry_patch workspace with an upstream_url",
				errorType: "workspace_mode_mismatch",
			}
		}
	}
	return "", &refusal{
		status:  http.StatusBadRequest,
		message: `target must be "origin" or "upstream"`,
	}
}
