package secrets

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// The secrets and variables API is four operations (create, list, update,
// delete) on two resources (secrets, variables) at three owner tiers (user,
// org, workspace). One handler, built from a resource, a tier and an
// operation, serves all 24 routes; the resolved variables of a workspace have
// a handler of their own. Every handler checks in the same order: a credential
// (401), the resource's scope for a PAT (403), the tier's owner (403 for an
// admin token on the user tier, 404 for an org the caller is not a member of,
// the workspace access rule), then the operation.

// --- Scope helpers ---

func canSecretsRead(auth *apikit.AuthInfo) bool {
	return wsaccess.HasScope(auth, "secrets:read", "secrets:write")
}
func canSecretsWrite(auth *apikit.AuthInfo) bool { return wsaccess.HasScope(auth, "secrets:write") }
func canVarsRead(auth *apikit.AuthInfo) bool {
	return wsaccess.HasScope(auth, "vars:read", "vars:write")
}
func canVarsWrite(auth *apikit.AuthInfo) bool { return wsaccess.HasScope(auth, "vars:write") }

// requireUserIdentity rejects credentials that carry no user identity (admin
// tokens) on user-scoped endpoints. Admin tokens have UserID == "", so letting
// them through would read and write a phantom ("user", "") scope that no real
// user can ever see. Returns false after writing the error response.
func requireUserIdentity(c echo.Context, auth *apikit.AuthInfo) bool {
	if auth.UserID == "" {
		_ = respondError(c, http.StatusForbidden,
			"user-scoped endpoints require a user credential; admin tokens have no user identity")
		return false
	}
	return true
}

// respondError writes a JSON error envelope and sets the HTTP status code.
func respondError(c echo.Context, code int, message string) error {
	return c.JSON(code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

// checkOrgMembership verifies that the org exists and the user is a member.
// Returns the orgID and (0, "") if the check passes, or ("", httpCode, message) on failure.
func checkOrgMembership(db *sql.DB, userID, orgSlug string) (string, int, string) {
	var orgID string
	err := db.QueryRow("SELECT id FROM orgs WHERE slug = ?", orgSlug).Scan(&orgID)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}

	var isMember int
	err = db.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID).Scan(&isMember)
	if err != nil || isMember == 0 {
		return "", http.StatusNotFound, "not found"
	}

	return orgID, 0, ""
}

// isOrgMember reports whether userID is a member of the org identified by
// its ID. Any lookup error is treated as "not a member".
func isOrgMember(db *sql.DB, orgID, userID string) bool {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// authorizeWorkspace applies wsaccess.AuthorizeWorkspace with the route's
// need to a workspace-scoped secret or variable route: members of the
// workspace's org read its variables, its owner and admin tokens also write
// them and reach its secrets; a member's variable write is 403 forbidden, and
// a member on a secret route gets the stranger's 404. It writes the refusal
// and returns nil when access is denied.
func authorizeWorkspace(c echo.Context, db *sql.DB, auth *apikit.AuthInfo, slug string, need wsaccess.Need) *wsaccess.WorkspaceAccess {
	ws, code, msg := wsaccess.AuthorizeWorkspace(db, auth, slug, need)
	if code != 0 {
		_ = wsaccess.WriteRefusal(c, code, msg)
		return nil
	}
	return ws
}

// --- Request types ---

// createRequest is the JSON body for POST secrets/variables endpoints.
type createRequest struct {
	Entries []EntryInput `json:"entries"`
}

// updateSecretRequest is the JSON body for PATCH secrets endpoints.
// Value is a pointer to distinguish between missing/null (nil) and empty string.
type updateSecretRequest struct {
	Value *string `json:"value"`
}

// --- Error classification ---

// classifyStoreError maps a store-layer error to the appropriate HTTP status code.
// ConflictError → 409, NotFoundError → 404, wrapped DB errors → 500, validation → 400.
func classifyStoreError(err error) int {
	var ce *ConflictError
	if errors.As(err, &ce) {
		return http.StatusConflict
	}
	var nfe *NotFoundError
	if errors.As(err, &nfe) {
		return http.StatusNotFound
	}
	// Wrapped errors (fmt.Errorf("...: %w", dbErr)) are internal/DB errors.
	if errors.Unwrap(err) != nil {
		return http.StatusInternalServerError
	}
	// Everything else is a validation or limit error.
	return http.StatusBadRequest
}

// storeErrorMessage returns the error message to send in the response.
// Internal errors get a generic message; client errors pass through.
func storeErrorMessage(err error, code int) string {
	if code == http.StatusInternalServerError {
		return "internal server error"
	}
	return err.Error()
}

// --- Scope resolution helpers ---

// resolveOrgScope resolves an org slug to its UUID and verifies access.
// Admin bypasses the membership check; other credential types require membership.
func resolveOrgScope(db *sql.DB, auth *apikit.AuthInfo, slug string) (string, int, string) {
	if wsaccess.IsAdmin(auth) {
		var orgID string
		err := db.QueryRow("SELECT id FROM orgs WHERE slug = ?", slug).Scan(&orgID)
		if err != nil {
			return "", http.StatusNotFound, "not found"
		}
		return orgID, 0, ""
	}
	return checkOrgMembership(db, auth.UserID, slug)
}

// --- Resources, tiers and operations ---

// resource is secrets or variables: the scopes that read and write it, the
// access a workspace-tier request needs, its store operations and its audit
// events.
type resource struct {
	name     string
	canRead  func(*apikit.AuthInfo) bool
	canWrite func(*apikit.AuthInfo) bool
	// workspaceNeed is the access a workspace-tier request needs: secrets
	// are private to the owner; variables are read by members of the
	// workspace's org and written by the owner.
	workspaceNeed func(write bool) wsaccess.Need
	create        func(s *Store, ownerType, ownerID string, entries []EntryInput) (any, error)
	list          func(s *Store, ownerType, ownerID string) (any, error)
	update        func(s *Store, ownerType, ownerID, key, value string) (any, error)
	remove        func(s *Store, ownerType, ownerID, key string) error
	// event is the audit event prefix, "hub.secret" or "hub.variable"; emit
	// records one event of it.
	event string
	emit  func(c echo.Context, eventType, scope, key string)
}

var secretsResource = resource{
	name:          "secrets",
	canRead:       canSecretsRead,
	canWrite:      canSecretsWrite,
	workspaceNeed: func(bool) wsaccess.Need { return wsaccess.NeedPrivate },
	create: func(s *Store, ownerType, ownerID string, entries []EntryInput) (any, error) {
		return s.CreateSecrets(ownerType, ownerID, entries)
	},
	list: func(s *Store, ownerType, ownerID string) (any, error) { return s.ListSecrets(ownerType, ownerID) },
	update: func(s *Store, ownerType, ownerID, key, value string) (any, error) {
		return s.UpdateSecret(ownerType, ownerID, key, value)
	},
	remove: func(s *Store, ownerType, ownerID, key string) error { return s.DeleteSecret(ownerType, ownerID, key) },
	event:  "hub.secret",
	emit:   emitSecretAudit,
}

var varsResource = resource{
	name:     "vars",
	canRead:  canVarsRead,
	canWrite: canVarsWrite,
	workspaceNeed: func(write bool) wsaccess.Need {
		if write {
			return wsaccess.NeedWrite
		}
		return wsaccess.NeedRead
	},
	create: func(s *Store, ownerType, ownerID string, entries []EntryInput) (any, error) {
		return s.CreateVariables(ownerType, ownerID, entries)
	},
	list: func(s *Store, ownerType, ownerID string) (any, error) { return s.ListVariables(ownerType, ownerID) },
	update: func(s *Store, ownerType, ownerID, key, value string) (any, error) {
		return s.UpdateVariable(ownerType, ownerID, key, value)
	},
	remove: func(s *Store, ownerType, ownerID, key string) error { return s.DeleteVariable(ownerType, ownerID, key) },
	event:  "hub.variable",
	emit:   emitVarAudit,
}

// tier is who owns the entries a route addresses: the authenticated user, an
// org named by its slug, or a workspace named by its slug. owner answers the
// owner id of the request, or writes the refusal and returns false.
type tier struct {
	ownerType string
	path      string // the route prefix, with the slug parameter where the tier has one
	owner     func(c echo.Context, db *sql.DB, auth *apikit.AuthInfo, r resource, write bool) (string, bool)
}

var userTier = tier{
	ownerType: "user",
	path:      "/user",
	owner: func(c echo.Context, _ *sql.DB, auth *apikit.AuthInfo, _ resource, _ bool) (string, bool) {
		if !requireUserIdentity(c, auth) {
			return "", false
		}
		return auth.UserID, true
	},
}

var orgTier = tier{
	ownerType: "org",
	path:      "/orgs/:slug",
	owner: func(c echo.Context, db *sql.DB, auth *apikit.AuthInfo, _ resource, _ bool) (string, bool) {
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			_ = respondError(c, code, msg)
			return "", false
		}
		return orgID, true
	},
}

var workspaceTier = tier{
	ownerType: "workspace",
	path:      "/workspaces/:slug",
	owner: func(c echo.Context, db *sql.DB, auth *apikit.AuthInfo, r resource, write bool) (string, bool) {
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, r.workspaceNeed(write)) == nil {
			return "", false
		}
		return slug, true
	},
}

// operation is one of create, list, update and delete; write says which
// scope and workspace access it needs.
type operation struct {
	write bool
	run   func(c echo.Context, store *Store, r resource, ownerType, ownerID string) error
}

var opCreate = operation{write: true, run: func(c echo.Context, store *Store, r resource, ownerType, ownerID string) error {
	var req createRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}
	entries, err := r.create(store, ownerType, ownerID, req.Entries)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	for _, entry := range req.Entries {
		r.emit(c, r.event+".create", ownerType, entry.Key)
	}
	return c.JSON(http.StatusCreated, entries)
}}

var opList = operation{write: false, run: func(c echo.Context, store *Store, r resource, ownerType, ownerID string) error {
	entries, err := r.list(store, ownerType, ownerID)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "internal server error")
	}
	return c.JSON(http.StatusOK, entries)
}}

var opUpdate = operation{write: true, run: func(c echo.Context, store *Store, r resource, ownerType, ownerID string) error {
	key := c.Param("key")
	var req updateSecretRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}
	if req.Value == nil {
		return respondError(c, http.StatusBadRequest, "value field is required")
	}
	entry, err := r.update(store, ownerType, ownerID, key, *req.Value)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	r.emit(c, r.event+".update", ownerType, key)
	return c.JSON(http.StatusOK, entry)
}}

var opDelete = operation{write: true, run: func(c echo.Context, store *Store, r resource, ownerType, ownerID string) error {
	key := c.Param("key")
	if err := r.remove(store, ownerType, ownerID, key); err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	r.emit(c, r.event+".delete", ownerType, key)
	return c.NoContent(http.StatusNoContent)
}}

// handle is the handler of one operation on one resource at one tier.
func handle(store *Store, db *sql.DB, t tier, r resource, op operation) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		allowed := r.canRead
		if op.write {
			allowed = r.canWrite
		}
		if wsaccess.IsPAT(auth) && !allowed(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		ownerID, ok := t.owner(c, db, auth, r, op.write)
		if !ok {
			return nil
		}
		return op.run(c, store, r, t.ownerType, ownerID)
	}
}

// --- Resolved variables handler ---

func handleResolvedWorkspaceVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}

		slug := c.Param("slug")
		// A member of the workspace's org reads the owner's document: the
		// resolution describes what the workspace runs with.
		ws := authorizeWorkspace(c, db, auth, slug, wsaccess.NeedRead)
		if ws == nil {
			return nil
		}
		wsOwnerID, orgID := ws.OwnerID, ws.OrgID

		// The org tier is only visible to org members, matching the org
		// endpoints. A workspace whose owner has since left the org must not
		// keep receiving the org's variables through resolution.
		if orgID != "" && !wsaccess.IsAdmin(auth) && !isOrgMember(db, orgID, wsOwnerID) {
			orgID = ""
		}

		resolved, err := store.ResolveVariables(wsOwnerID, orgID, slug)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}

		return c.JSON(http.StatusOK, resolved)
	}
}
