package mergequeue

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// ---------------------------------------------------------------------------
// HTTP test infrastructure for merge queue handler tests
// ---------------------------------------------------------------------------

// mergeHTTPTestEnv provides an HTTP test environment for merge queue handler
// tests, including an in-memory database and echo router with routes registered.
type mergeHTTPTestEnv struct {
	e  *echo.Echo
	db *sql.DB
}

// newMergeHTTPTestEnv creates a test environment with an in-memory database,
// echo router with test auth middleware, and merge queue routes registered.
func newMergeHTTPTestEnv(t *testing.T) *mergeHTTPTestEnv {
	t.Helper()
	db := openTestDBNoSchema(t)
	setupMergeJobsTable(t, db)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(mergeTestAuthMiddleware())

	if err := RegisterMergeRoutes(api, db); err != nil {
		t.Fatalf("RegisterMergeRoutes() returned error: %v", err)
	}

	return &mergeHTTPTestEnv{e: e, db: db}
}

// doMergeRequest makes an HTTP request to the test merge server.
func (env *mergeHTTPTestEnv) doMergeRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if auth != nil {
		authJSON, err := json.Marshal(auth)
		if err != nil {
			t.Fatalf("failed to marshal auth info: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(authJSON))
	}
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	return rec
}

// mergeTestAuthMiddleware returns Echo middleware that reads auth info
// from the X-Test-Auth header and injects it into the echo context via
// apikit.SetAuthInfo.
func mergeTestAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("X-Test-Auth")
			if authHeader != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(authHeader), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// mergeWriteAuth returns an AuthInfo for a user with merges:write scope.
func mergeWriteAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		Permissions:    []string{"merges:write"},
	}
}

// mergeReadAuth returns an AuthInfo for a user with merges:read scope.
func mergeReadAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		Permissions:    []string{"merges:read"},
	}
}

// newMergeHTTPTestEnvWithCampaigns creates a test environment with both
// merge_jobs and campaign tables. Used for tests that need campaign_id
// and spec_id inference from source_ref.
func newMergeHTTPTestEnvWithCampaigns(t *testing.T) *mergeHTTPTestEnv {
	t.Helper()
	db := openTestDBNoSchema(t)
	setupMergeJobsTable(t, db)
	setupCampaignTables(t, db)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(mergeTestAuthMiddleware())

	if err := RegisterMergeRoutes(api, db); err != nil {
		t.Fatalf("RegisterMergeRoutes() returned error: %v", err)
	}

	return &mergeHTTPTestEnv{e: e, db: db}
}
