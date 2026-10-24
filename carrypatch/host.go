package carrypatch

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"regexp"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// This file is the module's contract with the host that embeds it. The
// carry-patch module knows nothing of the hub but what is declared here: the
// host hands it the per-workspace locks and the workspace access rule with
// SetHost, hands every handler an Emitter for its audit events, and adapts
// the git hooks the module builds (PreReceiveHook, PostPushHook) to its git
// server.

// Need is what a route requires of the caller's access to a workspace: to read
// it, or to change it.
type Need string

// Needs.
const (
	NeedRead  Need = "read"
	NeedWrite Need = "write"
)

// Host is what the module takes from the process that embeds it.
type Host struct {
	// Lock takes the per-workspace lock, waiting for it, and returns the
	// function that releases it. The lock is the one the host's own
	// operations on the workspace clone take (sync, archive, merge, push).
	Lock func(slug string) (unlock func())
	// TryLock takes the per-workspace lock if it is free.
	TryLock func(slug string) (unlock func(), ok bool)
	// BeginRebuild marks a rebuild as active on the workspace, so that the
	// host refuses to delete the clone while it runs, and returns the function
	// that ends the mark; ok is false while another rebuild is active.
	BeginRebuild func(slug string) (end func(), ok bool)
	// Authorize decides whether the caller may use the workspace for need. It
	// returns (0, "") when it may, else the HTTP status and message to answer
	// with: 401 without a credential, 404 for a workspace the caller may not
	// see, 403 for a caller who may read but not write.
	Authorize func(db *sql.DB, auth *apikit.AuthInfo, slug string, need Need) (status int, message string)
}

// host is the Host the embedding process set. The module does nothing useful
// before SetHost: every route, job and hook that takes a lock or decides access
// goes through it.
var host Host

// SetHost binds the module to its host. The hub calls it once at start, before
// any route is registered or job handled; tests bind their doubles the same
// way.
func SetHost(h Host) {
	host = h
}

// authorizeWorkspace applies the host's workspace access rule to a route and
// writes the refusal when access is denied: members of the workspace's org
// read, owners and admin tokens also write, a member's write is 403 and anyone
// else gets 404. It returns false when it wrote a refusal.
func authorizeWorkspace(c echo.Context, db *sql.DB, auth *apikit.AuthInfo, slug string, need Need) bool {
	if host.Authorize == nil {
		_ = apikit.WriteAPIError(c, http.StatusInternalServerError, "carry-patch host is not configured")
		return false
	}
	code, msg := host.Authorize(db, auth, slug, need)
	if code == 0 {
		return true
	}
	if code == http.StatusForbidden {
		_ = apikit.WriteAPIErrorWithType(c, code, msg, "forbidden")
	} else {
		_ = apikit.WriteAPIError(c, code, msg)
	}
	return false
}

// Event is an audit event the module records through the host's Emitter: the
// fields of the hub's audit event, without the identity and time the host adds.
type Event struct {
	EventType    string
	ActorID      string
	ActorType    string // one of: admin_token, api_key, pat, system
	ResourceType string
	ResourceID   string
	Action       string
	Workspace    string
	Metadata     map[string]any
}

// Emitter records audit events. A nil Emitter skips emission; an emit error
// is logged by the caller and changes no outcome.
type Emitter interface {
	Emit(ctx context.Context, event Event) error
}

// The audit events the module emits besides hub.rebuild.enqueue, complete and
// fail and hub.patch.create, delete and reset.
const (
	// EventRebuildFollowup is emitted when a rebuild enqueues a follow-up
	// rebuild because the patch list changed while it ran.
	EventRebuildFollowup = "hub.rebuild.followup"
	// EventPatchMirrorFailed is emitted when a hub-mode mirror of a patch
	// branch to the fork fails after a push.
	EventPatchMirrorFailed = "hub.patch.mirror_failed"
	// EventPatchSync is emitted once per carry-patch sync in origin mode with
	// the per-patch outcomes.
	EventPatchSync = "hub.patch.sync"
	// EventPatchReplace is emitted for each patch branch a sync replaced with
	// the fork's copy.
	EventPatchReplace = "hub.patch.replace"
	// EventPatchReset is emitted when a patch branch is reset to the fork's
	// tip through the reset-to-origin route.
	EventPatchReset = "hub.patch.reset"
)

// RefUpdate describes one reference update of a git push: the ref and the
// hashes it moves from and to. It is the host's git server's ref update.
type RefUpdate struct {
	Name plumbing.ReferenceName
	Old  plumbing.Hash
	New  plumbing.Hash
}

// PreReceiveHook is called by the host's git server once per ref update, after
// the pack is unpacked and before the ref is written. A non-nil error rejects
// that one update; its text is the per-ref status the client sees.
type PreReceiveHook func(ctx context.Context, db *sql.DB, slug string, actor *apikit.AuthInfo, upd RefUpdate) error

// PostPushHook is called by the host's git server after a push, with the
// branches it updated. It runs asynchronously; it logs its own failures.
type PostPushHook func(db *sql.DB, slug string, branches []string)

// isPAT reports whether the credential is a personal access token, the one
// credential type whose scopes are checked.
func isPAT(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "pat"
}

// hasScope reports whether the credential carries any of the scopes.
func hasScope(auth *apikit.AuthInfo, scopes ...string) bool {
	for _, s := range scopes {
		if slices.Contains(auth.Permissions, s) {
			return true
		}
	}
	return false
}

// userinfoPattern matches the scheme and the user information of a URL
// embedded anywhere in a string; group 1 is the scheme and "://", which is
// kept.
var userinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s?#]*@`)

// stripUserinfoText removes the user information (user:password@) of every
// URL in free-form text, such as an error message that embeds a remote URL,
// and returns everything else byte for byte. It is idempotent.
func stripUserinfoText(text string) string {
	return userinfoPattern.ReplaceAllString(text, "${1}")
}

// respondError writes an apikit error envelope.
func respondError(c echo.Context, code int, message string) error {
	return apikit.WriteAPIError(c, code, message)
}

// respondErrorWithType writes an apikit error envelope with an error_type.
func respondErrorWithType(c echo.Context, code int, message, errorType string) error {
	return apikit.WriteAPIErrorWithType(c, code, message, errorType)
}

// emitAudit records an event of a request through the emitter, with the
// caller as the actor (the system when the request has no credential). A nil
// emitter skips it; a failure is logged and changes nothing else.
func emitAudit(c echo.Context, emitter Emitter, event Event) {
	if emitter == nil {
		return
	}
	if auth := apikit.GetAuthInfo(c); auth != nil {
		event.ActorID = auth.UserID
		event.ActorType = auth.CredentialType
	} else {
		event.ActorID = ""
		event.ActorType = "system"
	}
	if err := emitter.Emit(c.Request().Context(), event); err != nil {
		slog.Error("audit: failed to emit event",
			"event_type", event.EventType,
			"workspace", event.Workspace,
			"error", err,
		)
	}
}

// workspaceRow is what the patch routes read of the host's workspaces table:
// the state that decides whether a patch operation may run.
type workspaceRow struct {
	Slug              string
	Status            string
	WorkspaceMode     string
	IntegrationBranch *string
	Branch            *string
	GitURL            string
}

// loadWorkspaceRow reads the workspace row the patch routes decide on. The
// caller has authorized the request, so a missing row is an error.
func loadWorkspaceRow(db *sql.DB, slug string) (*workspaceRow, error) {
	var ws workspaceRow
	err := db.QueryRow(
		`SELECT slug, status, workspace_mode, integration_branch, branch, git_url
		 FROM workspaces WHERE slug = ?`, slug,
	).Scan(&ws.Slug, &ws.Status, &ws.WorkspaceMode, &ws.IntegrationBranch, &ws.Branch, &ws.GitURL)
	if err != nil {
		return nil, err
	}
	return &ws, nil
}
