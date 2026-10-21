package wsaccess

import (
	"database/sql"
	"errors"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// bindingSchemaSQL creates the binding table and its slug index.
var bindingSchemaSQL = []string{
	`CREATE TABLE IF NOT EXISTS token_workspace_bindings (
		token_id   TEXT PRIMARY KEY,
		slug       TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_token_workspace_bindings_slug ON token_workspace_bindings(slug)`,
}

// ErrorTypeTokenBound is the error_type of the apikit error envelope a bound
// token receives on a route that cannot be scoped to its workspace.
const ErrorTypeTokenBound = "token_bound"

// InitBindingSchema creates the token_workspace_bindings table, which binds a
// personal access token (by apikit's token id) to one workspace slug, and its
// slug index. It is idempotent and leaves existing rows unchanged.
//
// The table has no foreign key to workspaces: deleting or archiving a
// workspace leaves its bindings in place, so a token bound to a deleted
// workspace reaches nothing rather than being silently unbound.
func InitBindingSchema(db *sql.DB) error {
	for _, stmt := range bindingSchemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// LookupBinding returns the slug tokenID is bound to. A token without a row
// is unbound (bound false, nil error); any other database error is returned
// so that callers fail closed.
//
// It is one primary-key lookup whose row is closed before it returns, so it
// can run on a single-connection pool as long as the caller holds no open
// rows or transaction on the same handle.
func LookupBinding(db *sql.DB, tokenID string) (slug string, bound bool, err error) {
	err = db.QueryRow(`SELECT slug FROM token_workspace_bindings WHERE token_id = ?`, tokenID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return slug, true, nil
}

// RefusalSite is where a binding refusal happens: the request's method and
// route template (empty where the refusal point has no request), the status
// of the refusal and its surface.
type RefusalSite struct {
	Method  string
	Route   string
	Status  int
	Surface string
}

// SiteOf is the RefusalSite of an API request answered with status.
func SiteOf(c echo.Context, status int) RefusalSite {
	return RefusalSite{Method: c.Request().Method, Route: c.Path(), Status: status, Surface: SurfaceAPI}
}

// DeniesWorkspace reports whether the credential in auth is a PAT bound to a
// workspace other than slug. It is the rule every enforcing layer applies
// (spec 33): to such a token, slug does not exist. A denial is a refusal, so
// it is reported once to the refusal observer with the given site (spec 34).
// Credentials that cannot be bound are never denied and cause no query. A
// failed lookup is returned with denied true, and the caller must deny.
func DeniesWorkspace(db *sql.DB, auth *apikit.AuthInfo, slug string, site RefusalSite) (bool, error) {
	boundSlug, bound, err := BoundSlug(db, auth)
	if err != nil {
		return true, err
	}
	if !bound || boundSlug == slug {
		return false, nil
	}
	ReportBoundRefusal(BoundRefusal{
		TokenID:       auth.TokenID,
		UserID:        auth.UserID,
		BoundSlug:     boundSlug,
		RequestedSlug: slug,
		Method:        site.Method,
		Route:         site.Route,
		Status:        site.Status,
		Surface:       site.Surface,
	})
	return true, nil
}

// BoundSlug resolves the binding of the credential in auth. Only a PAT with a
// token id can be bound: for a nil auth, an api_key, an admin_token or a PAT
// without a TokenID it returns ("", false, nil) without touching db.
func BoundSlug(db *sql.DB, auth *apikit.AuthInfo) (string, bool, error) {
	if auth == nil || auth.CredentialType != "pat" || auth.TokenID == "" {
		return "", false, nil
	}
	return LookupBinding(db, auth.TokenID)
}
