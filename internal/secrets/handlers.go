package secrets

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// --- Auth helpers using apikit.AuthInfo directly ---

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

// --- Core secret CRUD operations ---

func doCreateSecrets(c echo.Context, store *Store, ownerType, ownerID string) error {
	var req createRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}
	entries, err := store.CreateSecrets(ownerType, ownerID, req.Entries)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	// 18-REQ-4.1: Emit hub.secret.create for each created entry.
	for _, entry := range req.Entries {
		emitSecretAudit(c, "hub.secret.create", ownerType, entry.Key)
	}
	return c.JSON(http.StatusCreated, entries)
}

func doListSecrets(c echo.Context, store *Store, ownerType, ownerID string) error {
	entries, err := store.ListSecrets(ownerType, ownerID)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "internal server error")
	}
	return c.JSON(http.StatusOK, entries)
}

func doUpdateSecret(c echo.Context, store *Store, ownerType, ownerID string) error {
	key := c.Param("key")
	var req updateSecretRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}
	if req.Value == nil {
		return respondError(c, http.StatusBadRequest, "value field is required")
	}
	entry, err := store.UpdateSecret(ownerType, ownerID, key, *req.Value)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	// 18-REQ-4.2: Emit hub.secret.update after successful update.
	emitSecretAudit(c, "hub.secret.update", ownerType, key)
	return c.JSON(http.StatusOK, entry)
}

func doDeleteSecret(c echo.Context, store *Store, ownerType, ownerID string) error {
	key := c.Param("key")
	err := store.DeleteSecret(ownerType, ownerID, key)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	// 18-REQ-4.3: Emit hub.secret.delete after successful deletion.
	emitSecretAudit(c, "hub.secret.delete", ownerType, key)
	return c.NoContent(http.StatusNoContent)
}

// --- User-scoped secret handlers ---

func handleCreateUserSecrets(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		if !requireUserIdentity(c, auth) {
			return nil
		}
		return doCreateSecrets(c, store, "user", auth.UserID)
	}
}

func handleListUserSecrets(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		if !requireUserIdentity(c, auth) {
			return nil
		}
		return doListSecrets(c, store, "user", auth.UserID)
	}
}

func handleUpdateUserSecret(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		if !requireUserIdentity(c, auth) {
			return nil
		}
		return doUpdateSecret(c, store, "user", auth.UserID)
	}
}

func handleDeleteUserSecret(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		if !requireUserIdentity(c, auth) {
			return nil
		}
		return doDeleteSecret(c, store, "user", auth.UserID)
	}
}

// --- Org-scoped secret handlers ---

func handleCreateOrgSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doCreateSecrets(c, store, "org", orgID)
	}
}

func handleListOrgSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doListSecrets(c, store, "org", orgID)
	}
}

func handleUpdateOrgSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doUpdateSecret(c, store, "org", orgID)
	}
}

func handleDeleteOrgSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doDeleteSecret(c, store, "org", orgID)
	}
}

// --- Workspace-scoped secret handlers ---

func handleCreateWorkspaceSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, wsaccess.NeedPrivate) == nil {
			return nil
		}
		return doCreateSecrets(c, store, "workspace", slug)
	}
}

func handleListWorkspaceSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, wsaccess.NeedPrivate) == nil {
			return nil
		}
		return doListSecrets(c, store, "workspace", slug)
	}
}

func handleUpdateWorkspaceSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, wsaccess.NeedPrivate) == nil {
			return nil
		}
		return doUpdateSecret(c, store, "workspace", slug)
	}
}

func handleDeleteWorkspaceSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, wsaccess.NeedPrivate) == nil {
			return nil
		}
		return doDeleteSecret(c, store, "workspace", slug)
	}
}

// --- Core variable CRUD operations ---

func doCreateVars(c echo.Context, store *Store, ownerType, ownerID string) error {
	var req createRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}
	entries, err := store.CreateVariables(ownerType, ownerID, req.Entries)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	// 18-REQ-4.4: Emit hub.variable.create for each created entry.
	for _, entry := range req.Entries {
		emitVarAudit(c, "hub.variable.create", ownerType, entry.Key)
	}
	return c.JSON(http.StatusCreated, entries)
}

func doListVars(c echo.Context, store *Store, ownerType, ownerID string) error {
	entries, err := store.ListVariables(ownerType, ownerID)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "internal server error")
	}
	return c.JSON(http.StatusOK, entries)
}

func doUpdateVar(c echo.Context, store *Store, ownerType, ownerID string) error {
	key := c.Param("key")
	var req updateSecretRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}
	if req.Value == nil {
		return respondError(c, http.StatusBadRequest, "value field is required")
	}
	entry, err := store.UpdateVariable(ownerType, ownerID, key, *req.Value)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	// 18-REQ-4.5: Emit hub.variable.update after successful update.
	emitVarAudit(c, "hub.variable.update", ownerType, key)
	return c.JSON(http.StatusOK, entry)
}

func doDeleteVar(c echo.Context, store *Store, ownerType, ownerID string) error {
	key := c.Param("key")
	err := store.DeleteVariable(ownerType, ownerID, key)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	// 18-REQ-4.6: Emit hub.variable.delete after successful deletion.
	emitVarAudit(c, "hub.variable.delete", ownerType, key)
	return c.NoContent(http.StatusNoContent)
}

// --- User-scoped variable handlers ---

func handleCreateUserVars(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		if !requireUserIdentity(c, auth) {
			return nil
		}
		return doCreateVars(c, store, "user", auth.UserID)
	}
}

func handleListUserVars(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		if !requireUserIdentity(c, auth) {
			return nil
		}
		return doListVars(c, store, "user", auth.UserID)
	}
}

func handleUpdateUserVar(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		if !requireUserIdentity(c, auth) {
			return nil
		}
		return doUpdateVar(c, store, "user", auth.UserID)
	}
}

func handleDeleteUserVar(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		if !requireUserIdentity(c, auth) {
			return nil
		}
		return doDeleteVar(c, store, "user", auth.UserID)
	}
}

// --- Org-scoped variable handlers ---

func handleCreateOrgVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doCreateVars(c, store, "org", orgID)
	}
}

func handleListOrgVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doListVars(c, store, "org", orgID)
	}
}

func handleUpdateOrgVar(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doUpdateVar(c, store, "org", orgID)
	}
}

func handleDeleteOrgVar(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		orgID, code, msg := resolveOrgScope(db, auth, c.Param("slug"))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doDeleteVar(c, store, "org", orgID)
	}
}

// --- Workspace-scoped variable handlers ---

func handleCreateWorkspaceVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, wsaccess.NeedWrite) == nil {
			return nil
		}
		return doCreateVars(c, store, "workspace", slug)
	}
}

func handleListWorkspaceVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, wsaccess.NeedRead) == nil {
			return nil
		}
		return doListVars(c, store, "workspace", slug)
	}
}

func handleUpdateWorkspaceVar(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, wsaccess.NeedWrite) == nil {
			return nil
		}
		return doUpdateVar(c, store, "workspace", slug)
	}
}

func handleDeleteWorkspaceVar(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if wsaccess.IsPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		if authorizeWorkspace(c, db, auth, slug, wsaccess.NeedWrite) == nil {
			return nil
		}
		return doDeleteVar(c, store, "workspace", slug)
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
