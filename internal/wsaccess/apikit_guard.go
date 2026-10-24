package wsaccess

import (
	"database/sql"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// APIKitRouteGuard returns the deny-only middleware for the root Echo
// instance that refuses bound tokens on apikit's own routes.
//
// apikit registers its routes before hub can add middleware to the API
// group, so BindingGuard never runs on them. This guard runs on the root
// instance instead, after routing and before apikit authenticates. For a
// request whose method and path template match one of routes and whose
// Bearer header names a PAT id (see patTokenID) with a binding row it answers
// 403 token_bound without naming the workspace, because the credential is not
// authenticated yet. Every other request goes on unchanged, and requests on
// other routes or without a PAT id cause no query.
//
// It only refuses: it never sets AuthInfo, never grants access and never
// looks at the token's secret. A failed lookup answers 500.
func APIKitRouteGuard(db *sql.DB, routes []*echo.Route) echo.MiddlewareFunc {
	recorded := make(map[string]bool, len(routes))
	for _, r := range routes {
		recorded[routeKey(r.Method, r.Path)] = true
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !recorded[routeKey(c.Request().Method, c.Path())] {
				return next(c)
			}
			id := patTokenID(c.Request().Header.Get("Authorization"))
			if id == "" {
				return next(c)
			}
			slug, bound, err := LookupBinding(db, id)
			if err != nil {
				return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
			}
			if bound {
				// The token is not authenticated here, so the report has
				// no user.
				ReportBoundRefusal(BoundRefusal{
					TokenID:   id,
					BoundSlug: slug,
					Method:    c.Request().Method,
					Route:     c.Path(),
					Status:    http.StatusForbidden,
					Surface:   SurfaceAPI,
				})
				return apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
					"this token is bound to a workspace", ErrorTypeTokenBound)
			}
			return next(c)
		}
	}
}
