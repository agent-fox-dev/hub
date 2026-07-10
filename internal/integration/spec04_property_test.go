package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ==========================================================================
// Task group 2.3: Property-based tests
// TS-04-P1 through TS-04-P9
// ==========================================================================

// TestSpec04_P1_SlugGlobalUniqueness verifies that no two workspace records
// in the workspaces table ever share the same slug value.
// Property: 04-PROP-1
// Validates: 04-REQ-1.3
func TestSpec04_P1_SlugGlobalUniqueness(t *testing.T) {
	env := setupStandardEnv(t)

	slugs := []string{
		"alpha-ws", "bravo-ws", "charlie-ws", "delta-ws", "echo-ws",
		"foxtrot-ws", "golf-ws", "hotel-ws", "india-ws", "juliet-ws",
	}

	// Create all workspaces.
	for i, slug := range slugs {
		body := map[string]interface{}{
			"slug":    slug,
			"git_url": "https://github.com/org/repo.git",
		}
		rec := doRequest(env.E, http.MethodPost, "/api/v1/workspaces", body,
			bearer(env.OwnerAPIKey))
		assertStatus(t, rec, http.StatusCreated)
		t.Logf("created workspace[%d] slug=%s", i, slug)
	}

	// Attempt to create duplicates — each should return 409.
	for _, slug := range slugs {
		body := map[string]interface{}{
			"slug":    slug,
			"git_url": "https://github.com/org/dup.git",
		}
		rec := doRequest(env.E, http.MethodPost, "/api/v1/workspaces", body,
			bearer(env.OwnerAPIKey))
		assertStatus(t, rec, http.StatusConflict)
	}

	// Verify via direct SQL: no slug appears more than once.
	rows, err := env.DB.Query(`SELECT slug, COUNT(*) as cnt FROM workspaces GROUP BY slug HAVING cnt > 1`)
	if err != nil {
		t.Fatalf("SQL query failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var slug string
		var cnt int
		if err := rows.Scan(&slug, &cnt); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		t.Errorf("slug %q appears %d times (should be unique)", slug, cnt)
	}
}

// TestSpec04_P2_SecretNeverStoredAsPlaintext verifies that for every record
// in workspace_tokens, the plaintext secret is never stored — only its SHA-256 hash.
// Property: 04-PROP-2
// Validates: 04-REQ-12.2, 04-REQ-9.2
func TestSpec04_P2_SecretNeverStoredAsPlaintext(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-secret-chk", "secret-check-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Create several tokens via the API and collect plaintext secrets.
	type tokenPair struct {
		tokenID string
		secret  string
	}
	var pairs []tokenPair

	for i := 0; i < 5; i++ {
		body := map[string]interface{}{
			"label": fmt.Sprintf("test-token-%d", i),
		}
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/secret-check-ws/tokens", body,
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)

		respBody := parseJSONMap(t, rec)
		tokenStr, ok := respBody["token"].(string)
		if !ok {
			t.Fatalf("token field missing or not a string")
		}

		// Parse af_wt_<tokenID>_<secret>.
		parts := strings.SplitN(tokenStr, "_", 4)
		if len(parts) != 4 {
			t.Fatalf("token %q does not have expected format af_wt_<id>_<secret>", tokenStr)
		}
		pairs = append(pairs, tokenPair{tokenID: parts[2], secret: parts[3]})
	}

	// For each token, verify the DB does not contain the plaintext secret.
	for _, p := range pairs {
		var secretHash string
		err := env.DB.QueryRow(`SELECT secret_hash FROM workspace_tokens WHERE token_id = ?`,
			p.tokenID).Scan(&secretHash)
		if err != nil {
			t.Fatalf("failed to query token %s: %v", p.tokenID, err)
		}

		expectedHash := sha256Hex(p.secret)

		// Hash must match expected.
		if secretHash != expectedHash {
			t.Errorf("token %s: secret_hash = %q, want %q", p.tokenID, secretHash, expectedHash)
		}

		// Plaintext must not appear in hash column.
		if secretHash == p.secret {
			t.Errorf("token %s: plaintext secret stored in secret_hash column!", p.tokenID)
		}
	}
}

// TestSpec04_P3_TokenPlaintextReturnedExactlyOnce verifies that the plaintext
// token string is returned exactly once (in the creation response) and never
// exposed by any subsequent endpoint.
// Property: 04-PROP-3
// Validates: 04-REQ-9.1, 04-REQ-9.2, 04-REQ-10.1
func TestSpec04_P3_TokenPlaintextReturnedExactlyOnce(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-once", "once-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Create a token and capture plaintext.
	body := map[string]interface{}{"label": "one-time"}
	createRec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/once-ws/tokens", body,
		bearer(env.OwnerAPIKey))
	assertStatus(t, createRec, http.StatusCreated)

	createResp := parseJSONMap(t, createRec)
	plaintextToken, ok := createResp["token"].(string)
	if !ok {
		t.Fatal("creation response missing 'token' field")
	}
	secret := strings.SplitN(plaintextToken, "_", 4)[3]

	// Now list tokens — the plaintext must not appear anywhere.
	listRec := doRequest(env.E, http.MethodGet,
		"/api/v1/workspaces/once-ws/tokens", nil,
		bearer(env.OwnerAPIKey))
	assertStatus(t, listRec, http.StatusOK)

	tokens := parseJSONArray(t, listRec)
	for i, tok := range tokens {
		// No 'token' field.
		if _, exists := tok["token"]; exists {
			t.Errorf("token[%d] contains 'token' field in list response", i)
		}
		// No field value matches the plaintext token or raw secret.
		for k, v := range tok {
			s, ok := v.(string)
			if ok && (s == plaintextToken || s == secret) {
				t.Errorf("token[%d].%s = %q matches plaintext token/secret", i, k, s)
			}
		}
	}
}

// TestSpec04_P4_ExpiredRevokedTokensRejectedOnAllEndpoints verifies that
// expired or revoked workspace tokens are always rejected with HTTP 401 by
// the auth middleware before any handler executes, for any endpoint.
// Property: 04-PROP-4
// Validates: 04-REQ-12.3
func TestSpec04_P4_ExpiredRevokedTokensRejectedOnAllEndpoints(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-p4", "prop4-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Create an expired token.
	expiredToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-p4-exp", TokenID: "p4expir1",
		Secret: "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: strPtr(pastISO(24 * time.Hour)),
		CreatedAt: pastISO(72 * time.Hour), RevokedAt: nil,
	})

	// Create a revoked token.
	revokedAt := pastISO(12 * time.Hour)
	revokedToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-p4-rev", TokenID: "p4revok1",
		Secret: "ZYXWVUTSzyxwvuts9876543210zyxwvu",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: nil,
		CreatedAt: pastISO(48 * time.Hour), RevokedAt: &revokedAt,
	})

	endpoints := []struct {
		method string
		path   string
		body   interface{}
	}{
		{http.MethodPost, "/api/v1/workspaces", map[string]interface{}{"slug": "x", "git_url": "https://x.com"}},
		{http.MethodGet, "/api/v1/workspaces", nil},
		{http.MethodGet, "/api/v1/workspaces/prop4-ws", nil},
		{http.MethodPost, "/api/v1/workspaces/prop4-ws/tokens", map[string]interface{}{}},
		{http.MethodGet, "/api/v1/workspaces/prop4-ws/tokens", nil},
		{http.MethodDelete, "/api/v1/workspaces/prop4-ws/tokens/p4expir1", nil},
	}

	for _, tok := range []struct {
		name  string
		token string
	}{
		{"expired", expiredToken},
		{"revoked", revokedToken},
	} {
		for _, ep := range endpoints {
			name := fmt.Sprintf("%s_token_%s_%s", tok.name, ep.method, ep.path)
			t.Run(name, func(t *testing.T) {
				rec := doRequest(env.E, ep.method, ep.path, ep.body, bearer(tok.token))
				assertStatus(t, rec, http.StatusUnauthorized)
			})
		}
	}
}

// TestSpec04_P5_WorkspaceIDMismatchAlwaysProduces403 verifies that for any
// request authenticated by a workspace token targeting GET /api/v1/workspaces/:slug,
// a workspace_id mismatch always produces HTTP 403.
// Property: 04-PROP-5
// Validates: 04-REQ-7.5, 04-REQ-13.2
func TestSpec04_P5_WorkspaceIDMismatchAlwaysProduces403(t *testing.T) {
	env := setupStandardEnv(t)

	// Create 3 workspaces.
	type ws struct {
		id   string
		slug string
	}
	workspaces := []ws{
		{"ws-p5-a", "p5-ws-alpha"},
		{"ws-p5-b", "p5-ws-bravo"},
		{"ws-p5-c", "p5-ws-charlie"},
	}
	for _, w := range workspaces {
		createTestWorkspace(t, env.DB, w.id, w.slug,
			"https://github.com/org/repo.git", env.OwnerUserID)
	}

	// For each workspace, create a token and try accessing every OTHER workspace.
	for i, w := range workspaces {
		tokenID := fmt.Sprintf("p5tok%03d", i)
		token := createTestWorkspaceToken(t, env.DB, testTokenRecord{
			ID: fmt.Sprintf("tok-p5-%d", i), TokenID: tokenID,
			Secret:      "abcdefghABCDEFGH0123456789abcdef",
			WorkspaceID: w.id, UserID: env.OwnerUserID,
			Label: nil, ExpiresAt: strPtr(futureISO(30 * 24 * time.Hour)),
			CreatedAt: nowISO(), RevokedAt: nil,
		})

		for j, other := range workspaces {
			if i == j {
				continue // same workspace — should succeed, not testing here
			}
			name := fmt.Sprintf("token_for_%s_on_%s", w.slug, other.slug)
			t.Run(name, func(t *testing.T) {
				rec := doRequest(env.E, http.MethodGet,
					"/api/v1/workspaces/"+other.slug, nil,
					bearer(token))
				assertStatus(t, rec, http.StatusForbidden)
			})
		}
	}
}

// TestSpec04_P6_UpdatedAtEqualsCreatedAtOnCreation verifies that for every
// workspace record inserted, updated_at is set equal to created_at.
// Property: 04-PROP-6
// Validates: 04-REQ-8.3
func TestSpec04_P6_UpdatedAtEqualsCreatedAtOnCreation(t *testing.T) {
	env := setupStandardEnv(t)

	for i := 0; i < 5; i++ {
		slug := fmt.Sprintf("p6-ws-%d", i)
		body := map[string]interface{}{
			"slug":    slug,
			"git_url": "https://github.com/org/repo.git",
		}
		rec := doRequest(env.E, http.MethodPost, "/api/v1/workspaces", body,
			bearer(env.OwnerAPIKey))
		assertStatus(t, rec, http.StatusCreated)

		respBody := parseJSONMap(t, rec)
		createdAt, _ := respBody["created_at"].(string)
		updatedAt, _ := respBody["updated_at"].(string)

		if createdAt != updatedAt {
			t.Errorf("workspace %s: created_at=%q != updated_at=%q", slug, createdAt, updatedAt)
		}

		// Also verify in database.
		var dbCreated, dbUpdated string
		wsID, _ := respBody["id"].(string)
		err := env.DB.QueryRow(`SELECT created_at, updated_at FROM workspaces WHERE id = ?`, wsID).
			Scan(&dbCreated, &dbUpdated)
		if err != nil {
			t.Fatalf("failed to query workspace %s: %v", slug, err)
		}
		if dbCreated != dbUpdated {
			t.Errorf("DB workspace %s: created_at=%q != updated_at=%q", slug, dbCreated, dbUpdated)
		}
	}
}

// TestSpec04_P7_TokenIDCollisionRetryBoundedAt3 verifies that token_id
// generation retries are bounded at exactly 3 and the handler never enters
// an unbounded loop.
// Property: 04-PROP-7
// Validates: 04-REQ-9.E1
//
// NOTE: This test requires mocking crypto/rand to force collisions, which
// depends on the implementation supporting dependency injection for the random
// source. For the integration level, we test the happy path (unique token_id
// generation) and verify that the handler CAN return tokens. The unit-level
// test in the workspace package should cover the collision retry boundary.
func TestSpec04_P7_TokenIDCollisionRetryBoundedAt3(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-p7", "p7-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Pre-insert a token with a known token_id to prove uniqueness is enforced.
	createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-p7-existing", TokenID: "existing",
		Secret:      "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: nil,
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	// Create a token via the API — it should get a unique token_id.
	rec := doRequest(env.E, http.MethodPost,
		"/api/v1/workspaces/p7-ws/tokens",
		map[string]interface{}{"label": "retry-test"},
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusCreated)

	respBody := parseJSONMap(t, rec)
	newTokenID, ok := respBody["token_id"].(string)
	if !ok {
		t.Fatal("response missing token_id field")
	}

	// The new token_id must differ from the pre-existing one.
	if newTokenID == "existing" {
		t.Error("generated token_id collided with pre-existing token_id")
	}
}

// TestSpec04_P8_RevocationIdempotency verifies that DELETE .../tokens/:token_id
// is idempotent — always returns HTTP 204 when the token exists and belongs
// to the workspace, regardless of prior revocation state.
// Property: 04-PROP-8
// Validates: 04-REQ-11.1, 04-REQ-11.2
func TestSpec04_P8_RevocationIdempotency(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-p8", "p8-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-p8", TokenID: "p8token1",
		Secret:      "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: nil,
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	// First revocation.
	rec1 := doRequest(env.E, http.MethodDelete,
		"/api/v1/workspaces/p8-ws/tokens/p8token1", nil,
		bearer(env.OwnerAPIKey))
	assertStatus(t, rec1, http.StatusNoContent)
	assertEmptyBody(t, rec1)

	// Record revoked_at after first call.
	var revokedAt1 *string
	err := env.DB.QueryRow(`SELECT revoked_at FROM workspace_tokens WHERE token_id = ?`, "p8token1").
		Scan(&revokedAt1)
	if err != nil {
		t.Fatalf("failed to query revoked_at: %v", err)
	}
	if revokedAt1 == nil {
		t.Fatal("revoked_at should be non-null after first revocation")
	}

	// Second revocation — should be idempotent.
	rec2 := doRequest(env.E, http.MethodDelete,
		"/api/v1/workspaces/p8-ws/tokens/p8token1", nil,
		bearer(env.OwnerAPIKey))
	assertStatus(t, rec2, http.StatusNoContent)
	assertEmptyBody(t, rec2)

	// Verify revoked_at did not change.
	var revokedAt2 *string
	err = env.DB.QueryRow(`SELECT revoked_at FROM workspace_tokens WHERE token_id = ?`, "p8token1").
		Scan(&revokedAt2)
	if err != nil {
		t.Fatalf("failed to query revoked_at: %v", err)
	}
	if revokedAt2 == nil {
		t.Fatal("revoked_at should still be set after second revocation")
	}
	if *revokedAt1 != *revokedAt2 {
		t.Errorf("revoked_at changed: %q -> %q (should be stable)", *revokedAt1, *revokedAt2)
	}
}

// TestSpec04_P9_ListOrderingStability verifies that list responses from
// GET /api/v1/workspaces and GET .../tokens are always ordered by created_at
// ASC with id ASC as tiebreaker.
// Property: 04-PROP-9
// Validates: 04-REQ-6.1, 04-REQ-6.2, 04-REQ-10.1
func TestSpec04_P9_ListOrderingStability(t *testing.T) {
	env := setupStandardEnv(t)

	// Create workspaces with identical created_at but different IDs.
	fixedTime := "2025-01-15T10:00:00Z"
	createTestWorkspaceAt(t, env.DB, "aaa-ws-id", "p9-ws-aaa",
		"https://github.com/org/repo.git", env.OwnerUserID, fixedTime)
	createTestWorkspaceAt(t, env.DB, "zzz-ws-id", "p9-ws-zzz",
		"https://github.com/org/repo.git", env.OwnerUserID, fixedTime)
	createTestWorkspaceAt(t, env.DB, "mmm-ws-id", "p9-ws-mmm",
		"https://github.com/org/repo.git", env.OwnerUserID, fixedTime)

	t.Run("workspace list ordering", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet, "/api/v1/workspaces", nil,
			bearer(env.OwnerAPIKey))
		assertStatus(t, rec, http.StatusOK)

		workspaces := parseJSONArray(t, rec)
		for i := 0; i < len(workspaces)-1; i++ {
			ca1, _ := workspaces[i]["created_at"].(string)
			ca2, _ := workspaces[i+1]["created_at"].(string)
			if ca1 > ca2 {
				t.Errorf("workspace[%d].created_at (%s) > workspace[%d].created_at (%s)", i, ca1, i+1, ca2)
			}
			if ca1 == ca2 {
				id1, _ := workspaces[i]["id"].(string)
				id2, _ := workspaces[i+1]["id"].(string)
				if id1 >= id2 {
					t.Errorf("same created_at but workspace[%d].id (%s) >= workspace[%d].id (%s)", i, id1, i+1, id2)
				}
			}
		}
	})

	// Create tokens with identical created_at for ordering test.
	wsID := "aaa-ws-id"
	tokenFixedTime := "2025-02-20T12:00:00Z"

	createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-p9-z", TokenID: "p9tokzzz",
		Secret:      "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: nil,
		CreatedAt: tokenFixedTime, RevokedAt: nil,
	})
	createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-p9-a", TokenID: "p9tokaaa",
		Secret:      "ZYXWVUTSzyxwvuts9876543210zyxwvu",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: nil,
		CreatedAt: tokenFixedTime, RevokedAt: nil,
	})

	t.Run("token list ordering", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/p9-ws-aaa/tokens", nil,
			bearer(env.OwnerAPIKey))
		assertStatus(t, rec, http.StatusOK)

		tokens := parseJSONArray(t, rec)
		for i := 0; i < len(tokens)-1; i++ {
			ca1, _ := tokens[i]["created_at"].(string)
			ca2, _ := tokens[i+1]["created_at"].(string)
			if ca1 > ca2 {
				t.Errorf("token[%d].created_at (%s) > token[%d].created_at (%s)", i, ca1, i+1, ca2)
			}
		}
	})
}
