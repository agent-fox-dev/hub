package secrets

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
)

// authInfoKey is the echo context key for storing AuthInfo in test environments.
const authInfoKey = "secrets.auth"

// CredentialType identifies the type of credential used to authenticate a request.
type CredentialType string

const (
	// CredentialAdmin represents an admin token.
	CredentialAdmin CredentialType = "admin"
	// CredentialAPIKey represents a user API key.
	CredentialAPIKey CredentialType = "apikey"
	// CredentialPAT represents a personal access token.
	CredentialPAT CredentialType = "pat"
)

// AuthInfo holds the authenticated identity and permissions for a request.
type AuthInfo struct {
	CredType    CredentialType `json:"cred_type"`
	UserID      string         `json:"user_id"`
	Permissions []string       `json:"permissions"`
}

// getAuth retrieves the AuthInfo from the echo context.
func getAuth(c echo.Context) (*AuthInfo, error) {
	val := c.Get(authInfoKey)
	if val != nil {
		info, ok := val.(*AuthInfo)
		if ok {
			return info, nil
		}
	}
	return nil, echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
}

// hasPermission checks whether the AuthInfo contains a specific permission scope.
func (a *AuthInfo) hasPermission(perm string) bool {
	for _, p := range a.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// hasSecretsManage reports whether the credential can perform all secrets operations.
func (a *AuthInfo) hasSecretsManage() bool {
	return a.hasPermission("secrets:manage")
}

// hasSecretsList reports whether the credential can list secrets.
// secrets:manage implies secrets:list.
func (a *AuthInfo) hasSecretsList() bool {
	return a.hasPermission("secrets:list") || a.hasPermission("secrets:manage")
}

// hasSecretsWrite reports whether the credential can update secrets.
// secrets:manage implies secrets:write.
func (a *AuthInfo) hasSecretsWrite() bool {
	return a.hasPermission("secrets:write") || a.hasPermission("secrets:manage")
}

// hasSecretsDelete reports whether the credential can delete secrets.
// secrets:manage implies secrets:delete.
func (a *AuthInfo) hasSecretsDelete() bool {
	return a.hasPermission("secrets:delete") || a.hasPermission("secrets:manage")
}

// hasVarsManage reports whether the credential can perform all variables operations.
func (a *AuthInfo) hasVarsManage() bool {
	return a.hasPermission("vars:manage")
}

// hasVarsRead reports whether the credential can list and read variables.
// vars:manage and vars:write both imply vars:read.
func (a *AuthInfo) hasVarsRead() bool {
	return a.hasPermission("vars:read") ||
		a.hasPermission("vars:write") ||
		a.hasPermission("vars:manage")
}

// hasVarsWrite reports whether the credential can update variables.
// vars:manage implies vars:write.
func (a *AuthInfo) hasVarsWrite() bool {
	return a.hasPermission("vars:write") || a.hasPermission("vars:manage")
}

// hasVarsDelete reports whether the credential can delete variables.
// vars:manage implies vars:delete.
func (a *AuthInfo) hasVarsDelete() bool {
	return a.hasPermission("vars:delete") || a.hasPermission("vars:manage")
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
func lookupWorkspaceOwner(db *sql.DB, slug, userID string, isAdmin bool) (string, int, string) {
	var ownerID string
	err := db.QueryRow("SELECT owner_id FROM workspaces WHERE slug = ?", slug).Scan(&ownerID)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}
	if !isAdmin && ownerID != userID {
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
func resolveOrgScope(db *sql.DB, auth *AuthInfo, slug string) (string, int, string) {
	if auth.CredType == CredentialAdmin {
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
	return c.JSON(http.StatusOK, entry)
}

func doDeleteSecret(c echo.Context, store *Store, ownerType, ownerID string) error {
	key := c.Param("key")
	err := store.DeleteSecret(ownerType, ownerID, key)
	if err != nil {
		code := classifyStoreError(err)
		return respondError(c, code, storeErrorMessage(err, code))
	}
	return c.NoContent(http.StatusNoContent)
}

// --- User-scoped secret handlers ---

func handleCreateUserSecrets(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsManage() {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		return doCreateSecrets(c, store, "user", auth.UserID)
	}
}

func handleListUserSecrets(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsList() {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		return doListSecrets(c, store, "user", auth.UserID)
	}
}

func handleUpdateUserSecret(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsWrite() {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		return doUpdateSecret(c, store, "user", auth.UserID)
	}
}

func handleDeleteUserSecret(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsDelete() {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		return doDeleteSecret(c, store, "user", auth.UserID)
	}
}

// --- Org-scoped secret handlers ---

func handleCreateOrgSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsManage() {
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
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsList() {
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
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsWrite() {
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
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsDelete() {
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
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsManage() {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, auth.CredType == CredentialAdmin)
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doCreateSecrets(c, store, "workspace", slug)
	}
}

func handleListWorkspaceSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsList() {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, auth.CredType == CredentialAdmin)
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doListSecrets(c, store, "workspace", slug)
	}
}

func handleUpdateWorkspaceSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsWrite() {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, auth.CredType == CredentialAdmin)
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doUpdateSecret(c, store, "workspace", slug)
	}
}

func handleDeleteWorkspaceSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if auth.CredType == CredentialPAT && !auth.hasSecretsDelete() {
			return respondError(c, http.StatusForbidden, "insufficient permission scope")
		}
		slug := c.Param("slug")
		_, code, msg := lookupWorkspaceOwner(db, slug, auth.UserID, auth.CredType == CredentialAdmin)
		if code != 0 {
			return respondError(c, code, msg)
		}
		return doDeleteSecret(c, store, "workspace", slug)
	}
}

// --- User-scoped variable handlers ---

func handleCreateUserVars(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleListUserVars(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleUpdateUserVar(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleDeleteUserVar(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

// --- Org-scoped variable handlers ---

func handleCreateOrgVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleListOrgVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleUpdateOrgVar(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleDeleteOrgVar(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

// --- Workspace-scoped variable handlers ---

func handleCreateWorkspaceVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleListWorkspaceVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleUpdateWorkspaceVar(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleDeleteWorkspaceVar(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

// --- Resolved variables handler ---

func handleResolvedWorkspaceVars(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}
