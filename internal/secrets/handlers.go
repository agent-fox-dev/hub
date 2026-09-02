package secrets

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// --- Auth helpers using apikit.AuthInfo directly ---

func isAdmin(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "admin_token"
}

func isPAT(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "pat"
}

func hasScope(auth *apikit.AuthInfo, scopes ...string) bool {
	for _, s := range scopes {
		if slices.Contains(auth.Permissions, s) {
			return true
		}
	}
	return false
}

func canSecretsManage(auth *apikit.AuthInfo) bool { return hasScope(auth, "secrets:manage") }
func canSecretsList(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "secrets:list", "secrets:manage")
}
func canSecretsWrite(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "secrets:write", "secrets:manage")
}
func canSecretsDelete(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "secrets:delete", "secrets:manage")
}

func canVarsManage(auth *apikit.AuthInfo) bool { return hasScope(auth, "vars:manage") }
func canVarsRead(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "vars:read", "vars:write", "vars:manage")
}
func canVarsWrite(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "vars:write", "vars:manage")
}
func canVarsDelete(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "vars:delete", "vars:manage")
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

// lookupWorkspaceOwner retrieves a workspace and checks ownership.
// Returns the workspace owner_id or an error code for anti-enumeration.
func lookupWorkspaceOwner(db *sql.DB, slug, userID string, admin bool) (string, int, string) {
	var ownerID string
	err := db.QueryRow("SELECT owner_id FROM workspaces WHERE slug = ?", slug).Scan(&ownerID)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}
	if !admin && ownerID != userID {
		return "", http.StatusNotFound, "not found"
	}
	return ownerID, 0, ""
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
	if isAdmin(auth) {
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
		if isPAT(auth) && !canSecretsManage(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
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
		if isPAT(auth) && !canSecretsList(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
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
		if isPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
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
		if isPAT(auth) && !canSecretsDelete(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
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
		if isPAT(auth) && !canSecretsManage(auth) {
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
		if isPAT(auth) && !canSecretsList(auth) {
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
		if isPAT(auth) && !canSecretsWrite(auth) {
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
		if isPAT(auth) && !canSecretsDelete(auth) {
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
		if isPAT(auth) && !canSecretsManage(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
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
		if isPAT(auth) && !canSecretsList(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
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
		if isPAT(auth) && !canSecretsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
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
		if isPAT(auth) && !canSecretsDelete(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
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
		if isPAT(auth) && !canVarsManage(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
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
		if isPAT(auth) && !canVarsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
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
		if isPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
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
		if isPAT(auth) && !canVarsDelete(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
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
		if isPAT(auth) && !canVarsManage(auth) {
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
		if isPAT(auth) && !canVarsRead(auth) {
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
		if isPAT(auth) && !canVarsWrite(auth) {
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
		if isPAT(auth) && !canVarsDelete(auth) {
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
		if isPAT(auth) && !canVarsManage(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
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
		if isPAT(auth) && !canVarsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
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
		if isPAT(auth) && !canVarsWrite(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
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
		if isPAT(auth) && !canVarsDelete(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doDeleteVar(c, store, "workspace", slug)
	}
}

// --- Resolved variables handler ---

// lookupWorkspaceForResolution retrieves a workspace's owner_id and org_id
// for the resolved variables endpoint. Returns (ownerID, orgID, httpCode, message).
// orgID may be empty if the workspace has no org association.
func lookupWorkspaceForResolution(db *sql.DB, slug, userID string, admin bool) (string, string, int, string) {
	var ownerID string
	var orgID sql.NullString
	err := db.QueryRow(
		"SELECT owner_id, org_id FROM workspaces WHERE slug = ?", slug,
	).Scan(&ownerID, &orgID)
	if err != nil {
		return "", "", http.StatusNotFound, "not found"
	}
	if !admin && ownerID != userID {
		return "", "", http.StatusNotFound, "not found"
	}
	orgIDStr := ""
	if orgID.Valid {
		orgIDStr = orgID.String
	}
	return ownerID, orgIDStr, 0, ""
}

func handleResolvedWorkspaceVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !canVarsRead(auth) {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}

		slug := c.Param("slug")
		wsOwnerID, orgID, code, msg := lookupWorkspaceForResolution(db, slug, auth.UserID, isAdmin(auth))
		if code != 0 {
			return respondError(c, code, msg)
		}

		resolved, err := store.ResolveVariables(wsOwnerID, orgID, slug)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}

		return c.JSON(http.StatusOK, resolved)
	}
}
