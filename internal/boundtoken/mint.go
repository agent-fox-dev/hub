package boundtoken

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// mintRouteSuffix is the part of the mint route's template below the API
// group's mount point.
const mintRouteSuffix = "/workspaces/:slug/tokens"

// handleMint serves POST /workspaces/:slug/tokens (34-REQ-1, 34-REQ-2): it
// checks the caller in order, then mints the token through apikit's own
// POST <mount>/user/tokens, served in-process, so apikit's validation, expiry
// and privilege-escalation rules apply unchanged. Hub does no tokens:manage
// check of its own: apikit's refusal reaches the caller as it is.
func handleMint(cfg Config) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && refuseBoundToken(c, cfg.DB, auth) {
			return nil
		}
		if isAdmin(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden,
				"admin tokens cannot mint bound tokens; a real user is required as the token's owner")
		}
		slug := c.Param("slug")
		ws, code, msg := wsaccess.AuthorizeWorkspace(cfg.DB, auth, slug, wsaccess.NeedPrivate)
		if code != 0 {
			return apikit.WriteAPIError(c, code, msg)
		}
		// The owner, not merely a caller AuthorizeWorkspace lets read the
		// workspace, may mint for it (spec 34, Decision 4).
		if ws.OwnerID != auth.UserID {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}
		if ws.Status != "active" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "workspace is not active")
		}

		answer, err := delegateMint(c, cfg.APIKit)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		return finishMint(c, cfg, auth, answer)
	}
}

// delegateMint serves POST <mount>/user/tokens with apikit's handler, carrying
// the caller's body unchanged, its Authorization header verbatim, Content-Type
// and Content-Length, and its context, and returns apikit's answer recorded in
// memory. <mount> is the route template without mintRouteSuffix.
func delegateMint(c echo.Context, apikitHandler http.Handler) (*httptest.ResponseRecorder, error) {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSuffix(c.Path(), mintRouteSuffix) + "/user/tokens"
	inner, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	inner.RemoteAddr = c.Request().RemoteAddr
	inner.Header.Set("Authorization", c.Request().Header.Get("Authorization"))
	contentType := c.Request().Header.Get("Content-Type")
	if contentType == "" {
		contentType = echo.MIMEApplicationJSON
	}
	inner.Header.Set("Content-Type", contentType)
	inner.Header.Set("Content-Length", strconv.Itoa(len(body)))
	inner.ContentLength = int64(len(body))

	answer := httptest.NewRecorder()
	apikitHandler.ServeHTTP(answer, inner)
	return answer, nil
}

// minted is apikit's 201 answer to POST /user/tokens. Permissions and
// ExpiresAt are kept as apikit wrote them.
type minted struct {
	TokenID     string          `json:"token_id"`
	Name        string          `json:"name"`
	Token       string          `json:"token"`
	Permissions json.RawMessage `json:"permissions"`
	ExpiresAt   json.RawMessage `json:"expires_at"`
	CreatedAt   string          `json:"created_at"`
}

// mintResponse is the 201 body of POST /workspaces/:slug/tokens, the only
// place the plaintext token is ever returned.
type mintResponse struct {
	TokenID     string          `json:"token_id"`
	Token       string          `json:"token"`
	Name        string          `json:"name"`
	Permissions json.RawMessage `json:"permissions"`
	Workspace   string          `json:"workspace"`
	ExpiresAt   json.RawMessage `json:"expires_at"`
	CreatedAt   string          `json:"created_at"`
}

// msgBindFailed is the answer when a minted token could not be bound.
const msgBindFailed = "failed to bind the token to the workspace"

// finishMint answers the caller from apikit's recorded answer
// (34-REQ-2.3 to 34-REQ-3.7). Anything but a 201 is passed through unchanged.
// A 201 is bound to the workspace; when that fails for any reason the token
// is revoked before the answer, so no live unbound token is ever left. No
// path logs the token, apikit's body or the Authorization header.
func finishMint(c echo.Context, cfg Config, auth *apikit.AuthInfo, answer *httptest.ResponseRecorder) error {
	db := cfg.DB
	if answer.Code != http.StatusCreated {
		if ct := answer.Header().Get("Content-Type"); ct != "" {
			c.Response().Header().Set("Content-Type", ct)
		}
		c.Response().WriteHeader(answer.Code)
		_, err := c.Response().Write(answer.Body.Bytes())
		return err
	}

	slug := c.Param("slug")
	var m minted
	if err := json.Unmarshal(answer.Body.Bytes(), &m); err != nil || m.TokenID == "" || m.Token == "" {
		reason := errors.New("apikit's 201 answer lacks a token_id or a token")
		if err != nil {
			reason = errors.New("apikit's 201 answer is not JSON")
			m.TokenID = tokenIDOf(answer.Body.Bytes())
		}
		rollBack(db, m.TokenID, slug, reason)
		return apikit.WriteAPIError(c, http.StatusInternalServerError, msgBindFailed)
	}

	written, err := wsaccess.InsertBinding(db, m.TokenID, slug, apikit.NowUTC())
	if err != nil {
		rollBack(db, m.TokenID, slug, err)
		return apikit.WriteAPIError(c, http.StatusInternalServerError, msgBindFailed)
	}
	if !written {
		rollBack(db, m.TokenID, slug, errors.New("the workspace no longer exists"))
		return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
	}

	emitBind(c, cfg, auth, slug, m)
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusCreated, mintResponse{
		TokenID:     m.TokenID,
		Token:       m.Token,
		Name:        m.Name,
		Permissions: m.Permissions,
		Workspace:   slug,
		ExpiresAt:   m.ExpiresAt,
		CreatedAt:   m.CreatedAt,
	})
}

// emitBind records hub.token.bind for a committed binding (34-REQ-9.2), on a
// context the caller's disconnect does not cancel. A nil emitter skips it; an
// emit error is logged and does not change the answer.
func emitBind(c echo.Context, cfg Config, auth *apikit.AuthInfo, slug string, m minted) {
	if cfg.Audit == nil {
		return
	}
	permissions := []string{}
	if err := json.Unmarshal(m.Permissions, &permissions); err != nil || permissions == nil {
		permissions = []string{}
	}
	var expiresAt any
	var at string
	if json.Unmarshal(m.ExpiresAt, &at) == nil && at != "" {
		expiresAt = at
	}
	event := audit.HubEvent{
		EventType:    audit.EventTokenBind,
		ActorID:      auth.UserID,
		ActorType:    auth.CredentialType,
		ResourceType: "token",
		ResourceID:   m.TokenID,
		Action:       "bind",
		Workspace:    slug,
		Metadata: map[string]any{
			"token_id":    m.TokenID,
			"workspace":   slug,
			"permissions": permissions,
			"expires_at":  expiresAt,
		},
	}
	if err := cfg.Audit.Emit(context.WithoutCancel(c.Request().Context()), event); err != nil {
		slog.Error("boundtoken: emitting hub.token.bind failed", "token_id", m.TokenID, "workspace", slug, "error", err)
	}
}

// tokenIDOf reads the token_id of a 201 answer that is not valid JSON as far
// as the answer can be read, so that a token whose answer was cut short is
// still revoked. It returns "" when no token_id can be read.
func tokenIDOf(body []byte) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return ""
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return ""
		}
		if key == "token_id" {
			var id string
			if json.Unmarshal(value, &id) == nil {
				return id
			}
			return ""
		}
	}
	return ""
}

// rollBack revokes a minted token that could not be bound, when its id is
// known, and logs why with the token id only. A failed revocation is logged
// with both errors and does not change the answer.
func rollBack(db *sql.DB, tokenID, slug string, reason error) {
	if tokenID == "" {
		slog.Error("boundtoken: a minted token could not be bound and its id is unknown",
			"workspace", slug, "error", reason)
		return
	}
	if err := wsaccess.RevokeToken(db, tokenID); err != nil {
		slog.Error("boundtoken: a minted token could not be bound, and revoking it failed",
			"token_id", tokenID, "workspace", slug, "error", reason, "revoke_error", err)
		return
	}
	slog.Error("boundtoken: a minted token could not be bound and was revoked",
		"token_id", tokenID, "workspace", slug, "error", reason)
}
