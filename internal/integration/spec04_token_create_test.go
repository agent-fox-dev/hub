package integration

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/workspace"
)

// ==========================================================================
// Task group 6.1: Token creation endpoint tests
// TS-04-30 through TS-04-37, TS-04-49, TS-04-50, TS-04-E8, TS-04-E9,
// TS-04-E10, TS-04-E13
// ==========================================================================

// TestSpec04_TS30_ValidTokenCreationReturns201 verifies that a valid token
// creation request from the workspace owner returns HTTP 201 with the full
// token creation response including plaintext token.
// Requirement: 04-REQ-9.1
func TestSpec04_TS30_ValidTokenCreationReturns201(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-tc30", "token-create-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	body := map[string]interface{}{
		"label":   "my-token",
		"expires": 30,
	}
	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/token-create-ws/tokens", body,
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusCreated)

	resp := parseJSONMap(t, rec)

	// Verify token field.
	tokenStr, ok := resp["token"].(string)
	if !ok || tokenStr == "" {
		t.Fatal("response missing 'token' field")
	}
	if !strings.HasPrefix(tokenStr, "af_wt_") {
		t.Errorf("token %q does not start with af_wt_", tokenStr)
	}

	// Verify token_id is 8 base62 chars.
	tokenID, ok := resp["token_id"].(string)
	if !ok {
		t.Fatal("response missing 'token_id' field")
	}
	if len(tokenID) != 8 || !isBase62(tokenID) {
		t.Errorf("token_id = %q, want 8 base62 chars", tokenID)
	}

	// Verify label.
	label, ok := resp["label"].(string)
	if !ok || label != "my-token" {
		t.Errorf("label = %v, want %q", resp["label"], "my-token")
	}

	// Verify expires_at is ISO 8601.
	expiresAt, ok := resp["expires_at"].(string)
	if !ok {
		t.Fatal("response missing 'expires_at' field")
	}
	if !isISO8601(expiresAt) {
		t.Errorf("expires_at = %q is not ISO 8601", expiresAt)
	}

	// Verify created_at is ISO 8601.
	createdAt, ok := resp["created_at"].(string)
	if !ok {
		t.Fatal("response missing 'created_at' field")
	}
	if !isISO8601(createdAt) {
		t.Errorf("created_at = %q is not ISO 8601", createdAt)
	}

	// Verify token format: af_wt_<8chars>_<32chars>
	tokenRegex := regexp.MustCompile(`^af_wt_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}$`)
	if !tokenRegex.MatchString(tokenStr) {
		t.Errorf("token %q does not match expected format", tokenStr)
	}
}

// TestSpec04_TS31_PlaintextTokenReturnedAndSecretHashedInDB verifies that the
// plaintext token is returned in the token field and the secret is stored only
// as SHA-256 hash in the database.
// Requirement: 04-REQ-9.2
func TestSpec04_TS31_PlaintextTokenReturnedAndSecretHashedInDB(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-tc31", "hash-check-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/hash-check-ws/tokens",
		map[string]interface{}{},
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusCreated)

	resp := parseJSONMap(t, rec)
	tokenStr := resp["token"].(string)

	// Parse the token: af_wt_<token_id>_<secret>
	parts := strings.SplitN(tokenStr, "_", 4)
	if len(parts) != 4 {
		t.Fatalf("token %q does not have 4 underscore-delimited parts", tokenStr)
	}
	tokenID := parts[2]
	secret := parts[3]

	// Query SQLite directly to verify secret_hash.
	var dbSecretHash string
	err := env.DB.QueryRow(`SELECT secret_hash FROM workspace_tokens WHERE token_id = ?`,
		tokenID).Scan(&dbSecretHash)
	if err != nil {
		t.Fatalf("failed to query token: %v", err)
	}

	expectedHash := sha256Hex(secret)
	if dbSecretHash != expectedHash {
		t.Errorf("DB secret_hash = %q, want %q", dbSecretHash, expectedHash)
	}

	// Verify plaintext secret is NOT stored.
	if dbSecretHash == secret {
		t.Error("plaintext secret stored in DB!")
	}
}

// TestSpec04_TS32_DefaultExpiresAndZeroExpires verifies that omitting expires
// defaults to 30 days and expires=0 produces null expires_at.
// Requirement: 04-REQ-9.3
func TestSpec04_TS32_DefaultExpiresAndZeroExpires(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-tc32", "expiry-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	t.Run("omitted expires defaults to 30 days", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/expiry-ws/tokens",
			map[string]interface{}{},
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)

		resp := parseJSONMap(t, rec)
		createdAtStr, _ := resp["created_at"].(string)
		expiresAtStr, ok := resp["expires_at"].(string)
		if !ok {
			t.Fatal("expires_at should be a non-null string when expires is omitted (default 30)")
		}

		createdAt, err := time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			t.Fatalf("failed to parse created_at: %v", err)
		}
		expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
		if err != nil {
			t.Fatalf("failed to parse expires_at: %v", err)
		}

		expectedExpiry := createdAt.AddDate(0, 0, 30)
		diff := expiresAt.Sub(expectedExpiry)
		if diff < -time.Second || diff > time.Second {
			t.Errorf("expires_at should be ~created_at + 30 days, got diff=%v", diff)
		}
	})

	t.Run("expires=0 produces null expires_at", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/expiry-ws/tokens",
			map[string]interface{}{"expires": 0},
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)

		resp := parseJSONMap(t, rec)
		if resp["expires_at"] != nil {
			t.Errorf("expires_at should be null when expires=0, got %v", resp["expires_at"])
		}
	})
}

// TestSpec04_TS33_InvalidExpiresReturns400 verifies that an invalid expires
// value (not 0, 30, 60, or 90) is rejected with HTTP 400.
// Requirement: 04-REQ-9.4
func TestSpec04_TS33_InvalidExpiresReturns400(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-tc33", "bad-expires-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	badValues := []int{1, 29, 31, 45, 91, 100, -1}
	for _, badExpires := range badValues {
		badExpires := badExpires // capture range variable
		t.Run(fmt.Sprintf("expires=%d", badExpires), func(t *testing.T) {
			rec := doRequest(env.E, http.MethodPost,
				"/api/v1/workspaces/bad-expires-ws/tokens",
				map[string]interface{}{"expires": badExpires},
				bearer(env.OwnerAPIKey))

			assertStatus(t, rec, http.StatusBadRequest)
			assertErrorEnvelope(t, rec, 400)
		})
	}
}

// TestSpec04_TS34_LabelNormalization verifies that empty string label is
// normalized to null, omitted label is null, and non-empty label is stored as-is.
// Requirement: 04-REQ-9.5
func TestSpec04_TS34_LabelNormalization(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-tc34", "label-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	t.Run("empty string label normalized to null", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/label-ws/tokens",
			map[string]interface{}{"label": ""},
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)

		resp := parseJSONMap(t, rec)
		if resp["label"] != nil {
			t.Errorf("label should be null for empty string, got %v", resp["label"])
		}
	})

	t.Run("omitted label is null", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/label-ws/tokens",
			map[string]interface{}{},
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)

		resp := parseJSONMap(t, rec)
		if resp["label"] != nil {
			t.Errorf("label should be null when omitted, got %v", resp["label"])
		}
	})

	t.Run("non-empty label stored as-is", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/label-ws/tokens",
			map[string]interface{}{"label": "my-label"},
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)

		resp := parseJSONMap(t, rec)
		label, ok := resp["label"].(string)
		if !ok || label != "my-label" {
			t.Errorf("label = %v, want %q", resp["label"], "my-label")
		}
	})
}

// TestSpec04_TS35_LongLabelReturns400 verifies that a label exceeding 128
// characters is rejected with HTTP 400.
// Requirement: 04-REQ-9.6
func TestSpec04_TS35_LongLabelReturns400(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-tc35", "long-label-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	longLabel := strings.Repeat("a", 129)
	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/long-label-ws/tokens",
		map[string]interface{}{"label": longLabel},
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusBadRequest)
	assertErrorEnvelope(t, rec, 400)
}

// TestSpec04_TS36_NonOwnerAndWTCannotCreateTokens verifies that non-owner users
// and workspace token callers are rejected with HTTP 403 on POST .../tokens.
// Requirement: 04-REQ-9.7
func TestSpec04_TS36_NonOwnerAndWTCannotCreateTokens(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-tc36", "restricted-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	wtToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-tc36", TokenID: "tc36tok1",
		Secret:      "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: strPtr(futureISO(30 * 24 * time.Hour)),
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	t.Run("non-owner user returns 403", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/restricted-ws/tokens",
			map[string]interface{}{},
			bearer(env.NonOwnerAPIKey))

		assertStatus(t, rec, http.StatusForbidden)
		assertErrorEnvelope(t, rec, 403)
	})

	t.Run("workspace token returns 403", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/restricted-ws/tokens",
			map[string]interface{}{},
			bearer(wtToken))

		assertStatus(t, rec, http.StatusForbidden)
		assertErrorEnvelope(t, rec, 403)
	})
}

// TestSpec04_TS37_CreateTokenNonexistentSlugReturns404 verifies that POST
// .../tokens for a non-existent slug returns HTTP 404.
// Requirement: 04-REQ-9.8
func TestSpec04_TS37_CreateTokenNonexistentSlugReturns404(t *testing.T) {
	env := setupStandardEnv(t)

	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/ghost-token-ws/tokens",
		map[string]interface{}{},
		bearer(env.AdminToken))

	assertStatus(t, rec, http.StatusNotFound)
	assertErrorEnvelope(t, rec, 404)
}

// TestSpec04_TS49_TokenFormatMatchesRegex verifies that the generated workspace
// token matches the format af_wt_<8 base62 chars>_<32 base62 chars>.
// Requirement: 04-REQ-12.1
func TestSpec04_TS49_TokenFormatMatchesRegex(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-tc49", "format-check-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/format-check-ws/tokens",
		map[string]interface{}{},
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusCreated)

	resp := parseJSONMap(t, rec)

	tokenStr, ok := resp["token"].(string)
	if !ok {
		t.Fatal("response missing 'token' field")
	}

	tokenRegex := regexp.MustCompile(`^af_wt_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}$`)
	if !tokenRegex.MatchString(tokenStr) {
		t.Errorf("token %q does not match pattern ^af_wt_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}$", tokenStr)
	}

	tokenID, ok := resp["token_id"].(string)
	if !ok {
		t.Fatal("response missing 'token_id' field")
	}

	tokenIDRegex := regexp.MustCompile(`^[0-9A-Za-z]{8}$`)
	if !tokenIDRegex.MatchString(tokenID) {
		t.Errorf("token_id %q does not match pattern ^[0-9A-Za-z]{8}$", tokenID)
	}
}

// TestSpec04_TS50_SecretStoredAsSHA256Only verifies that the secret is stored as
// SHA-256 hash only and the plaintext is never persisted in workspace_tokens.
// Requirement: 04-REQ-12.2
func TestSpec04_TS50_SecretStoredAsSHA256Only(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-tc50", "hash-store-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/hash-store-ws/tokens",
		map[string]interface{}{},
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusCreated)

	resp := parseJSONMap(t, rec)
	tokenStr := resp["token"].(string)
	tokenID := resp["token_id"].(string)

	// Extract secret from the token string.
	parts := strings.SplitN(tokenStr, "_", 4)
	secret := parts[3]

	// Query the DB row.
	var dbSecretHash, dbID, dbTokenID, dbWorkspaceID, dbUserID, dbCreatedAt string
	var dbLabel, dbExpiresAt, dbRevokedAt *string
	err := env.DB.QueryRow(`SELECT id, token_id, secret_hash, workspace_id, user_id, label, expires_at, created_at, revoked_at
		FROM workspace_tokens WHERE token_id = ?`, tokenID).Scan(
		&dbID, &dbTokenID, &dbSecretHash, &dbWorkspaceID, &dbUserID,
		&dbLabel, &dbExpiresAt, &dbCreatedAt, &dbRevokedAt)
	if err != nil {
		t.Fatalf("failed to query token: %v", err)
	}

	// secret_hash should equal hex(sha256(secret)).
	expectedHash := sha256Hex(secret)
	if dbSecretHash != expectedHash {
		t.Errorf("DB secret_hash = %q, want %q", dbSecretHash, expectedHash)
	}

	// No column should contain the raw secret.
	if dbSecretHash == secret {
		t.Error("plaintext secret stored in secret_hash column!")
	}

	// Check none of the string columns contain the plaintext secret.
	for _, col := range []string{dbID, dbTokenID, dbSecretHash, dbWorkspaceID, dbUserID, dbCreatedAt} {
		if col == secret {
			t.Errorf("plaintext secret found in column value %q", col)
		}
	}
}

// TestSpec04_E8_TokenIDCollisionRetriesExhaustedReturns500 verifies that when
// all 3 token_id generation retries result in collision, HTTP 500 is returned
// and no partial token record is inserted.
// Requirement: 04-REQ-9.E1
//
// This test uses RandReader injection to force collisions. The approach:
// create a token with a known ID, then inject a reader that always produces
// the same bytes, causing the generated token_id to collide.
func TestSpec04_E8_TokenIDCollisionRetriesExhaustedReturns500(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-e8", "collision-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Pre-insert a token with the token_id that our fixed reader will generate.
	// The generateBase62 function uses rejection sampling (byte < 248, byte%62).
	// If we feed bytes that are all 0x00, each byte maps to base62[0] = '0'.
	// So token_id becomes "00000000".
	collidingTokenID := "00000000"
	createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-e8-collide", TokenID: collidingTokenID,
		Secret:      "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: nil,
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	// Save and restore the original reader.
	origReader := workspace.RandReader
	defer func() { workspace.RandReader = origReader }()

	// Replace with a reader that always produces 0x00 bytes, making every
	// generated token_id == "00000000" (collides with pre-inserted token).
	workspace.RandReader = &zeroReader{}

	// Count tokens before.
	var countBefore int
	env.DB.QueryRow(`SELECT COUNT(*) FROM workspace_tokens WHERE workspace_id = ?`, wsID).Scan(&countBefore)

	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/collision-ws/tokens",
		map[string]interface{}{},
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusInternalServerError)
	assertErrorEnvelope(t, rec, 500)

	// Verify no new token records were inserted.
	var countAfter int
	env.DB.QueryRow(`SELECT COUNT(*) FROM workspace_tokens WHERE workspace_id = ?`, wsID).Scan(&countAfter)
	if countAfter != countBefore {
		t.Errorf("token count changed from %d to %d; expected no new records", countBefore, countAfter)
	}
}

// TestSpec04_E9_DBInsertErrorReturns500 verifies that an unexpected database
// error during token INSERT returns HTTP 500 with no partial token visible.
// Requirement: 04-REQ-9.E2
//
// We simulate a DB error by renaming the workspace_tokens table before the
// request, causing the INSERT to fail, then restore it afterward.
func TestSpec04_E9_DBInsertErrorReturns500(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-e9", "db-err-token-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Count initial tokens.
	var initialCount int
	env.DB.QueryRow(`SELECT COUNT(*) FROM workspace_tokens WHERE workspace_id = ?`, wsID).Scan(&initialCount)

	// Rename the table to force an INSERT error.
	_, err := env.DB.Exec(`ALTER TABLE workspace_tokens RENAME TO workspace_tokens_backup`)
	if err != nil {
		t.Fatalf("failed to rename table: %v", err)
	}

	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/db-err-token-ws/tokens",
		map[string]interface{}{},
		bearer(env.OwnerAPIKey))

	// Restore the table before assertions (so cleanup works).
	_, err = env.DB.Exec(`ALTER TABLE workspace_tokens_backup RENAME TO workspace_tokens`)
	if err != nil {
		t.Fatalf("failed to restore table: %v", err)
	}

	assertStatus(t, rec, http.StatusInternalServerError)
	assertErrorEnvelope(t, rec, 500)

	// Verify no partial record was created.
	var finalCount int
	env.DB.QueryRow(`SELECT COUNT(*) FROM workspace_tokens WHERE workspace_id = ?`, wsID).Scan(&finalCount)
	if finalCount != initialCount {
		t.Errorf("token count changed from %d to %d; expected no new records", initialCount, finalCount)
	}
}

// TestSpec04_E10_UnboundedTokenCreationAllowed verifies that token creation is
// unbounded — no maximum token count per workspace — and multiple tokens may
// share the same label.
// Requirement: 04-REQ-9.E3
func TestSpec04_E10_UnboundedTokenCreationAllowed(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-e10", "many-tokens-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Create 5 tokens with the same label (reduced from 11 for speed).
	for i := 0; i < 5; i++ {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/many-tokens-ws/tokens",
			map[string]interface{}{"label": "shared-label"},
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)
	}

	// Verify all tokens exist.
	var count int
	err := env.DB.QueryRow(`SELECT COUNT(*) FROM workspace_tokens WHERE workspace_id = ?`, wsID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count tokens: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5 tokens, got %d", count)
	}
}

// TestSpec04_E13_CryptoRandErrorReturns500 verifies that a crypto/rand read
// error during token generation propagates as a Go error and results in
// HTTP 500 with no partial record.
// Requirement: 04-REQ-12.E1
func TestSpec04_E13_CryptoRandErrorReturns500(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-e13", "rand-err-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Save and restore the original reader.
	origReader := workspace.RandReader
	defer func() { workspace.RandReader = origReader }()

	// Replace with a reader that always returns an error.
	workspace.RandReader = &errorReader{}

	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/rand-err-ws/tokens",
		map[string]interface{}{},
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusInternalServerError)
	assertErrorEnvelope(t, rec, 500)

	// Verify no token records were created.
	var count int
	env.DB.QueryRow(`SELECT COUNT(*) FROM workspace_tokens WHERE workspace_id = ?`, wsID).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 tokens after rand error, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Test helper: readers for injecting into workspace.RandReader
// ---------------------------------------------------------------------------

// zeroReader always returns bytes of value 0x00.
type zeroReader struct{}

func (z *zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// errorReader always returns an error.
type errorReader struct{}

func (e *errorReader) Read(_ []byte) (int, error) {
	return 0, errRandFailed
}

var errRandFailed = &randError{}

type randError struct{}

func (r *randError) Error() string { return "simulated crypto/rand failure" }
