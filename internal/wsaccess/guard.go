package wsaccess

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// BindingGuard returns the post-authentication middleware that confines a
// bound token (spec 33). Install it on the API group after apikit's auth
// middleware and before any hub route is registered: echo copies group
// middleware into every route and subgroup added later, so every hub route,
// including ones added in the future, is covered.
//
// For a PAT the guard resolves the binding with one lookup. Credentials that
// are not PATs, and requests without credentials, pass without a query. A
// PAT whose AuthInfo carries no TokenID gets the id from its authenticated
// Bearer header (see patTokenID), written back with apikit.SetAuthInfo; a PAT
// without a derivable id is unbound. A bound token then
//
//   - on a workspace-scoped route (path template with workspaces/:slug) for
//     a slug other than its own gets 404 workspace not found, the answer a
//     non-owner gets, before the handler and so before any scope check;
//   - on its own workspace passes into the handler's own checks;
//   - on any other route passes only if the route was marked with
//     AllowBoundTokens, and otherwise gets 403 token_bound, which also
//     covers paths that match no route.
//
// A failed lookup answers 500 and never lets the token through as unbound.
func BindingGuard(db *sql.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			auth := apikit.GetAuthInfo(c)
			if auth == nil || auth.CredentialType != "pat" {
				return next(c)
			}
			if auth.TokenID == "" {
				id := patTokenID(c.Request().Header.Get("Authorization"))
				if id == "" {
					return next(c)
				}
				enriched := *auth
				enriched.TokenID = id
				apikit.SetAuthInfo(c, &enriched)
				auth = &enriched
			}

			slug, bound, err := LookupBinding(db, auth.TokenID)
			if err != nil {
				return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
			}
			if !bound {
				return next(c)
			}

			refusal := BoundRefusal{
				TokenID:   auth.TokenID,
				UserID:    auth.UserID,
				BoundSlug: slug,
				Method:    c.Request().Method,
				Route:     c.Path(),
				Surface:   SurfaceAPI,
			}
			if isWorkspaceScoped(c.Path()) {
				if c.Param("slug") != slug {
					refusal.RequestedSlug, refusal.Status = c.Param("slug"), http.StatusNotFound
					ReportBoundRefusal(refusal)
					return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
				}
				return next(c)
			}
			if IsAllowed(c.Request().Method, c.Path()) {
				return next(c)
			}
			refusal.Status = http.StatusForbidden
			ReportBoundRefusal(refusal)
			return apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
				"this token is bound to workspace "+slug, ErrorTypeTokenBound)
		}
	}
}

// isWorkspaceScoped reports whether a route path template contains the
// segment workspaces immediately followed by the segment :slug.
func isWorkspaceScoped(template string) bool {
	segments := strings.Split(template, "/")
	for i := 0; i+1 < len(segments); i++ {
		if segments[i] == "workspaces" && segments[i+1] == ":slug" {
			return true
		}
	}
	return false
}

// patTokenID returns the token id of a PAT presented as
// "Bearer <prefix>_pat_<token_id>_<secret>", where <prefix> is the token
// prefix apikit was built with and the scheme is matched case-insensitively.
// It returns "" for any other header, and for an empty id or one with no
// separator after it. It only parses: it never looks at the secret.
//
// apikit strips "Bearer " from the header and then once more from the token,
// so "Bearer Bearer <PAT>" authenticates the PAT. Every repeated scheme is
// therefore skipped here: the guard must find the id in every header apikit
// accepts.
func patTokenID(header string) string {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	token = strings.TrimSpace(token)
	for {
		scheme, rest, ok := strings.Cut(token, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			break
		}
		token = strings.TrimSpace(rest)
	}
	rest, ok := strings.CutPrefix(token, apikit.TokenPrefix+"_pat_")
	if !ok {
		return ""
	}
	id, _, ok := strings.Cut(rest, "_")
	if !ok || id == "" {
		return ""
	}
	return id
}
