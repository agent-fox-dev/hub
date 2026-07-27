package gitserver

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// TS-06-6: A request with a valid af_pat_ credential in the Basic auth
// password field is resolved to the PAT identity and passed to the next
// handler; the username field is ignored.
// Requirement: 06-REQ-2.1
func TestGitAuth_ValidPATCredential_Resolved(t *testing.T) {
	db := openTestDB(t)
	middleware := GitAuthMiddleware(db)

	var capturedInfo *apikit.AuthInfo
	handler := middleware(func(c echo.Context) error {
		// Capture the auth info that the middleware should have set.
		capturedInfo = apikit.GetAuthInfo(c)
		return c.NoContent(http.StatusOK)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", nil)
	// Use a valid af_pat_ prefixed credential in the password field.
	// The username field should be ignored per 06-REQ-2.E2.
	req.Header.Set("Authorization", basicAuthHeader("anyusername", "af_pat_abc123"))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	// The middleware must resolve the credential and attach the identity.
	if capturedInfo == nil {
		t.Fatal("apikit.GetAuthInfo(c) returned nil; want non-nil AuthInfo after valid PAT auth")
	}
	if capturedInfo.CredentialType != "pat" {
		t.Errorf("AuthInfo.CredentialType = %q; want %q", capturedInfo.CredentialType, "pat")
	}
}

// TS-06-6 (continued): Verify that the username field in Basic auth is
// ignored — any non-empty string is accepted alongside a valid password.
// Requirement: 06-REQ-2.E2
func TestGitAuth_UsernameIgnored(t *testing.T) {
	db := openTestDB(t)
	middleware := GitAuthMiddleware(db)

	usernames := []string{"x-token-auth", "anything", "foo@bar.com", ""}

	for _, username := range usernames {
		t.Run("username="+username, func(t *testing.T) {
			var capturedInfo *apikit.AuthInfo
			handler := middleware(func(c echo.Context) error {
				capturedInfo = apikit.GetAuthInfo(c)
				return c.NoContent(http.StatusOK)
			})

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet,
				"/git/myorg/myws.git/info/refs?service=git-upload-pack", nil)
			req.Header.Set("Authorization", basicAuthHeader(username, "af_pat_abc123"))
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			_ = handler(c)

			// The identity should be resolved regardless of the username value.
			if capturedInfo == nil {
				t.Errorf("apikit.GetAuthInfo(c) returned nil with username %q; want non-nil", username)
			}
		})
	}
}

// TS-06-7: A git request with no Authorization header receives HTTP 401 with
// WWW-Authenticate: Basic realm="af-hub" and no body.
// Requirement: 06-REQ-2.2
func TestGitAuth_NoAuthHeader_Returns401WithChallenge(t *testing.T) {
	db := openTestDB(t)
	middleware := GitAuthMiddleware(db)

	nextCalled := false
	handler := middleware(func(c echo.Context) error {
		nextCalled = true
		return c.NoContent(http.StatusOK)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", nil)
	// No Authorization header.
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	_ = handler(c)

	if nextCalled {
		t.Error("next handler should NOT be called when Authorization header is missing")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("response status = %d; want %d", rec.Code, http.StatusUnauthorized)
	}

	wwwAuth := rec.Header().Get("WWW-Authenticate")
	expected := `Basic realm="af-hub"`
	if wwwAuth != expected {
		t.Errorf("WWW-Authenticate = %q; want %q", wwwAuth, expected)
	}

	// The response body must be empty per the spec.
	body := rec.Body.String()
	if body != "" {
		t.Errorf("response body = %q; want empty", body)
	}
}

// TS-06-8: A git request with an unrecognized credential in the Basic auth
// password field returns HTTP 401 without a WWW-Authenticate header.
// Requirement: 06-REQ-2.3
func TestGitAuth_InvalidCredential_Returns401NoChallengeHeader(t *testing.T) {
	db := openTestDB(t)
	middleware := GitAuthMiddleware(db)

	nextCalled := false
	handler := middleware(func(c echo.Context) error {
		nextCalled = true
		return c.NoContent(http.StatusOK)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", nil)
	req.Header.Set("Authorization", basicAuthHeader("user", "af_key_invalid999"))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	_ = handler(c)

	if nextCalled {
		t.Error("next handler should NOT be called for invalid credentials")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("response status = %d; want %d", rec.Code, http.StatusUnauthorized)
	}

	// Per 06-REQ-2.3, there should be no WWW-Authenticate header for bad creds.
	wwwAuth := rec.Header().Get("WWW-Authenticate")
	if wwwAuth != "" {
		t.Errorf("WWW-Authenticate header = %q; want empty (no challenge for bad creds)",
			wwwAuth)
	}
}

// TestGitAuth_MalformedAuthHeader verifies that a malformed Authorization
// header (not valid Base64 or missing the colon separator) returns HTTP 401.
// Requirement: 06-REQ-2.E1
func TestGitAuth_MalformedAuthHeader(t *testing.T) {
	db := openTestDB(t)
	middleware := GitAuthMiddleware(db)

	tests := []struct {
		name      string
		authValue string
	}{
		{"not base64", "Basic !!!notbase64!!!"},
		{"no colon separator", "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon"))},
		{"empty base64", "Basic "},
		{"not Basic scheme", "Bearer sometoken"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled := false
			handler := middleware(func(c echo.Context) error {
				nextCalled = true
				return c.NoContent(http.StatusOK)
			})

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet,
				"/git/myorg/myws.git/info/refs?service=git-upload-pack", nil)
			req.Header.Set("Authorization", tc.authValue)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			_ = handler(c)

			if nextCalled {
				t.Errorf("next handler should NOT be called for malformed auth (%s)", tc.name)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("response status with %s = %d; want %d",
					tc.name, rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestGitAuth_AllCredentialPrefixes verifies that all three credential prefixes
// (af_pat_, af_key_, af_admin_) are accepted by the auth middleware and result
// in a resolved identity attached to the request context.
// Requirement: 06-REQ-2.1
func TestGitAuth_AllCredentialPrefixes(t *testing.T) {
	db := openTestDB(t)
	middleware := GitAuthMiddleware(db)

	prefixes := []struct {
		name             string
		token            string
		expectedCredType string
	}{
		{"af_pat_ prefix", "af_pat_test123", "pat"},
		{"af_key_ prefix", "af_key_test456", "api_key"},
		{"af_admin_ prefix", "af_admin_test789", "admin_token"},
	}

	for _, tc := range prefixes {
		t.Run(tc.name, func(t *testing.T) {
			var capturedInfo *apikit.AuthInfo
			handler := middleware(func(c echo.Context) error {
				capturedInfo = apikit.GetAuthInfo(c)
				return c.NoContent(http.StatusOK)
			})

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet,
				"/git/myorg/myws.git/info/refs?service=git-upload-pack", nil)
			req.Header.Set("Authorization", basicAuthHeader("x", tc.token))
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			_ = handler(c)

			// The middleware must resolve the credential and set AuthInfo.
			if capturedInfo == nil {
				t.Fatalf("apikit.GetAuthInfo(c) returned nil for %s; want non-nil", tc.name)
			}
			if capturedInfo.CredentialType != tc.expectedCredType {
				t.Errorf("CredentialType = %q; want %q", capturedInfo.CredentialType, tc.expectedCredType)
			}
		})
	}
}
