package workspace

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// maxBranchPushBody bounds the request body of the branch push, which holds at
// most {"force": true}.
const maxBranchPushBody = 64 << 10

// handleBranchPush handles POST /api/v1/workspaces/:slug/branches/:name/push:
// it pushes one branch of the workspace's trunk to its origin.
//
// Requirements: 38-REQ-1 to 38-REQ-8
func handleBranchPush(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// 38-REQ-2.1, 38-REQ-2.3: the scope is checked before the workspace
		// is looked up. A PAT bound to another workspace never gets here: the
		// binding guard answers 404 first (38-REQ-2.2).
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasWriteAccess(auth) {
			return respondError(c, http.StatusForbidden,
				"PAT requires workspaces:write scope to push branches")
		}

		// 38-REQ-2.4 to 38-REQ-2.6: write access, as for every workspace write.
		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth, wsaccess.NeedWrite)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		// 38-REQ-1.4, 38-REQ-1.5
		_, err := parseBranchPushBody(c.Request().Body)
		if err != nil {
			return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// 38-REQ-1.2, 38-REQ-3.1: the name is the segment as sent, decoded
		// once; a name git would not accept names no branch of the trunk.
		name, err := branchPushName(c.Request())
		if err == nil {
			err = gitcmd.ValidateRefName(name)
		}
		if err != nil {
			return respondErrorWithType(c, http.StatusNotFound,
				fmt.Sprintf("branch_not_found: %q is not a valid branch name: %v", name, err), "branch_not_found")
		}

		// 38-REQ-3.2, 38-REQ-3.3
		if ws.Status != "active" {
			return respondError(c, http.StatusBadRequest,
				"workspace is not active; push requires an active workspace")
		}
		if ws.CloneStatus != "ready" {
			return respondError(c, http.StatusBadRequest,
				"workspace clone is not ready; clone_status must be 'ready' to push")
		}

		return respondError(c, http.StatusNotImplemented, "branch push is not implemented yet")
	}
}

// parseBranchPushBody reads the optional body of the branch push and returns its
// force. An empty body, an object without force and unknown fields leave force
// false; a body that is not a JSON object, or a force that is not a boolean, is
// an error.
func parseBranchPushBody(r io.Reader) (bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBranchPushBody))
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return false, errors.New("the body must be a JSON object")
	}
	value, ok := body["force"]
	if !ok {
		return false, nil
	}
	var force bool
	if string(bytes.TrimSpace(value)) == "null" || json.Unmarshal(value, &force) != nil {
		return false, errors.New("force must be a boolean")
	}
	return force, nil
}

// branchPushName is the branch name of the request: the path segment before the
// final /push, percent-decoded once. It reads the path as the client sent it,
// because echo's own path parameter is still encoded when the path holds a %2F
// and decoded when it does not, so decoding that value would sometimes decode a
// name that holds a % twice.
func branchPushName(r *http.Request) (string, error) {
	path := strings.TrimSuffix(r.URL.EscapedPath(), "/push")
	return url.PathUnescape(path[strings.LastIndexByte(path, '/')+1:])
}
