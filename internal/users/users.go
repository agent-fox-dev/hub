// Package users implements user management endpoints for af-hub, providing
// user CRUD operations with admin and self-service access patterns.
//
// Endpoints:
//   - POST   /api/v1/users          (admin only)
//   - GET    /api/v1/users          (admin only)
//   - GET    /api/v1/users/:id      (admin only)
//   - PUT    /api/v1/users/:id      (mixed auth)
package users

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/agent-fox-dev/hub/internal/authctx"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// CredentialType identifies the kind of authentication credential used.
// Type alias for authctx.CredentialType.
type CredentialType = authctx.CredentialType

const (
	// CredentialTypeAdmin is an admin token credential.
	CredentialTypeAdmin = authctx.CredentialTypeAdmin
	// CredentialTypeAPIKey is a user API key credential.
	CredentialTypeAPIKey = authctx.CredentialTypeAPIKey
	// CredentialTypeWorkspaceToken is a workspace token credential.
	CredentialTypeWorkspaceToken = authctx.CredentialTypeWorkspaceToken
)

// ContextKey is a typed key for Echo context values.
type ContextKey = authctx.ContextKey

// AuthContextKey is the Echo context key for AuthContext.
const AuthContextKey = authctx.AuthContextKey

// AuthContext holds the authentication state set by auth middleware.
// Type alias for authctx.AuthContext.
type AuthContext = authctx.AuthContext

// User represents a user record in the users table.
type User struct {
	ID              string           `json:"id"`
	Username        string           `json:"username"`
	Email           string           `json:"email"`
	FullName        string           `json:"full_name"`
	Status          string           `json:"status"`
	Provider        string           `json:"provider"`
	ProviderID      string           `json:"provider_id,omitempty"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
	TeamMemberships []TeamMembership `json:"team_memberships,omitempty"`
}

// TeamMembership represents a user's membership in a team.
type TeamMembership struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	Role     string `json:"role"`
}

// CreateUserRequest is the request body for POST /api/v1/users.
type CreateUserRequest struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	FullName   string `json:"full_name"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
}

// UpdateUserRequest is the request body for PUT /api/v1/users/:id.
type UpdateUserRequest struct {
	FullName *string `json:"full_name,omitempty"`
	Status   *string `json:"status,omitempty"`
}

// ProviderRegistry is a minimal interface for checking provider registration.
type ProviderRegistry interface {
	IsRegistered(name string) bool
}

// usernameRegexp validates usernames: alphanumeric and hyphens only, 1-39 chars.
var usernameRegexp = regexp.MustCompile(`^[0-9A-Za-z-]{1,39}$`)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// errorResponse is the standard JSON error envelope.
type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// writeError writes a JSON error envelope response.
func writeError(c echo.Context, code int, message string) error {
	return c.JSON(code, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// getAuthContext extracts the AuthContext from the Echo context.
// Handles both pointer storage (from unit test helpers) and value storage
// (from real auth middleware).
func getAuthContext(c echo.Context) *AuthContext {
	v := c.Get(string(AuthContextKey))
	if v == nil {
		return nil
	}
	if ac, ok := v.(*AuthContext); ok {
		return ac
	}
	if ac, ok := v.(AuthContext); ok {
		return &ac
	}
	return nil
}

// ValidateUsername checks that a username contains only alphanumeric
// characters and hyphens, and is at most 39 characters long.
func ValidateUsername(username string) error {
	if !usernameRegexp.MatchString(username) {
		return fmt.Errorf("username must contain only alphanumeric characters and hyphens, max 39 characters")
	}
	return nil
}

// ---------------------------------------------------------------------------
// POST /api/v1/users — Admin creates a user (02-REQ-4.1 through 02-REQ-4.4)
// ---------------------------------------------------------------------------

// CreateUserHandler returns an Echo handler for POST /api/v1/users.
// Admin-only endpoint that creates a new user record without generating an
// API key. Returns HTTP 201 with user object excluding provider_id.
func CreateUserHandler(db *sql.DB, registry ProviderRegistry) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Check admin auth.
		ac := getAuthContext(c)
		if ac == nil || !ac.IsAdmin {
			return writeError(c, http.StatusForbidden, "admin access required")
		}

		// Parse request body.
		var req CreateUserRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, http.StatusBadRequest, "invalid request body")
		}

		// Validate username format (02-REQ-4.3, 02-REQ-12.1).
		if err := ValidateUsername(req.Username); err != nil {
			return writeError(c, http.StatusBadRequest, err.Error())
		}

		// Validate provider_id is non-empty (02-REQ-4.3).
		if req.ProviderID == "" {
			return writeError(c, http.StatusBadRequest, "provider_id is required and must be non-empty")
		}

		// Log warning for unregistered provider but proceed (02-REQ-4.2).
		if registry != nil && !registry.IsRegistered(req.Provider) {
			log.Printf("WARNING: provider %q is not registered in the provider registry", req.Provider)
		}

		// Check case-insensitive username uniqueness (02-REQ-4.4, 02-REQ-12.2).
		var conflictID string
		err := db.QueryRowContext(c.Request().Context(),
			`SELECT id FROM users WHERE LOWER(username) = LOWER(?)`,
			req.Username,
		).Scan(&conflictID)
		if err == nil {
			return writeError(c, http.StatusConflict, "username already exists (case-insensitive)")
		} else if err != sql.ErrNoRows {
			return writeError(c, http.StatusInternalServerError, "failed to check username uniqueness")
		}

		// Check (provider, provider_id) uniqueness (02-REQ-4.4).
		err = db.QueryRowContext(c.Request().Context(),
			`SELECT id FROM users WHERE provider = ? AND provider_id = ?`,
			req.Provider, req.ProviderID,
		).Scan(&conflictID)
		if err == nil {
			return writeError(c, http.StatusConflict, "provider and provider_id combination already exists")
		} else if err != sql.ErrNoRows {
			return writeError(c, http.StatusInternalServerError, "failed to check provider uniqueness")
		}

		// Generate UUID v4 id and timestamps.
		id := uuid.New().String()
		now := time.Now().UTC().Format(time.RFC3339)

		// Insert the user record.
		_, err = db.ExecContext(c.Request().Context(),
			`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?)`,
			id, req.Username, req.Email, req.FullName, req.Provider, req.ProviderID, now, now,
		)
		if err != nil {
			return writeError(c, http.StatusInternalServerError, "failed to create user record")
		}

		// Return HTTP 201 with user object excluding provider_id (02-REQ-4.1).
		// ProviderID left as zero value "" → omitted by omitempty tag.
		// TeamMemberships left nil → omitted by omitempty tag.
		resp := User{
			ID:        id,
			Username:  req.Username,
			Email:     req.Email,
			FullName:  req.FullName,
			Status:    "active",
			Provider:  req.Provider,
			CreatedAt: now,
			UpdatedAt: now,
		}

		return c.JSON(http.StatusCreated, resp)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/users — Admin lists all users (02-REQ-5.1, 02-REQ-5.2)
// ---------------------------------------------------------------------------

// ListUsersHandler returns an Echo handler for GET /api/v1/users.
// Admin-only endpoint returning all user records ordered by created_at ASC,
// with provider_id omitted from each entry.
func ListUsersHandler(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Check admin auth.
		ac := getAuthContext(c)
		if ac == nil || !ac.IsAdmin {
			return writeError(c, http.StatusForbidden, "admin access required")
		}

		rows, err := db.QueryContext(c.Request().Context(),
			`SELECT id, username, email, full_name, status, provider, created_at, updated_at
			 FROM users ORDER BY created_at ASC`)
		if err != nil {
			return writeError(c, http.StatusInternalServerError, "failed to query users")
		}
		defer rows.Close()

		// Initialize as empty slice to ensure JSON array [] not null.
		userList := make([]User, 0)
		for rows.Next() {
			var u User
			if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.FullName,
				&u.Status, &u.Provider, &u.CreatedAt, &u.UpdatedAt); err != nil {
				return writeError(c, http.StatusInternalServerError, "failed to scan user row")
			}
			// ProviderID left as "" → omitted by omitempty.
			userList = append(userList, u)
		}
		if err := rows.Err(); err != nil {
			return writeError(c, http.StatusInternalServerError, "failed to iterate user rows")
		}

		return c.JSON(http.StatusOK, map[string]any{
			"users": userList,
		})
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/users/:id — Admin gets user by ID (02-REQ-6.1)
// ---------------------------------------------------------------------------

// GetUserHandler returns an Echo handler for GET /api/v1/users/:id.
// Admin-only endpoint returning a single user record including provider_id
// and team_memberships array. The role field in team memberships is hardcoded
// to "member" because the team_members table has no role column (see errata).
func GetUserHandler(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Check admin auth.
		ac := getAuthContext(c)
		if ac == nil || !ac.IsAdmin {
			return writeError(c, http.StatusForbidden, "admin access required")
		}

		userID := c.Param("id")

		// Query user by ID.
		var u User
		err := db.QueryRowContext(c.Request().Context(),
			`SELECT id, username, email, full_name, status, provider, provider_id, created_at, updated_at
			 FROM users WHERE id = ?`,
			userID,
		).Scan(&u.ID, &u.Username, &u.Email, &u.FullName,
			&u.Status, &u.Provider, &u.ProviderID, &u.CreatedAt, &u.UpdatedAt)

		if err == sql.ErrNoRows {
			return writeError(c, http.StatusNotFound, "user not found")
		}
		if err != nil {
			return writeError(c, http.StatusInternalServerError, "failed to query user")
		}

		// Fetch team memberships via JOIN with teams table (02-REQ-6.1).
		// The role field is hardcoded to "member" since team_members table
		// has no role column (spec 03 deferred roles; see errata doc).
		rows, err := db.QueryContext(c.Request().Context(),
			`SELECT tm.team_id, t.name
			 FROM team_members tm
			 JOIN teams t ON tm.team_id = t.id
			 WHERE tm.user_id = ?`,
			userID,
		)
		if err != nil {
			return writeError(c, http.StatusInternalServerError, "failed to query team memberships")
		}
		defer rows.Close()

		// Initialize as empty non-nil slice so JSON serializes as [] not null.
		memberships := make([]TeamMembership, 0)
		for rows.Next() {
			var tm TeamMembership
			if err := rows.Scan(&tm.TeamID, &tm.TeamName); err != nil {
				return writeError(c, http.StatusInternalServerError, "failed to scan team membership")
			}
			tm.Role = "member" // Hardcoded default; see errata doc.
			memberships = append(memberships, tm)
		}
		if err := rows.Err(); err != nil {
			return writeError(c, http.StatusInternalServerError, "failed to iterate team memberships")
		}

		u.TeamMemberships = memberships

		return c.JSON(http.StatusOK, u)
	}
}

// ---------------------------------------------------------------------------
// PUT /api/v1/users/:id — Mixed-auth user update
// (02-REQ-7.1 through 02-REQ-7.4, 02-REQ-13.1)
// ---------------------------------------------------------------------------

// UpdateUserHandler returns an Echo handler for PUT /api/v1/users/:id.
// Any authenticated user can update their own full_name. Admins can update
// status on any user. updated_at is bumped only when a field value actually
// changes (02-REQ-13.1).
//
// Response includes provider_id but excludes team_memberships.
func UpdateUserHandler(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Extract auth context.
		ac := getAuthContext(c)
		if ac == nil {
			return writeError(c, http.StatusForbidden, "authentication required")
		}

		targetID := c.Param("id")

		// Parse request body.
		var req UpdateUserRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, http.StatusBadRequest, "invalid request body")
		}

		// 02-REQ-7.E2: Non-admin including status field → 403 (even on own record).
		if !ac.IsAdmin && req.Status != nil {
			return writeError(c, http.StatusForbidden, "only admins can update user status")
		}

		// 02-REQ-7.E1: Non-admin updating another user's record → 403.
		if !ac.IsAdmin && ac.UserID != targetID {
			return writeError(c, http.StatusForbidden, "cannot update another user's record")
		}

		// Load current user from DB.
		var u User
		err := db.QueryRowContext(c.Request().Context(),
			`SELECT id, username, email, full_name, status, provider, provider_id, created_at, updated_at
			 FROM users WHERE id = ?`,
			targetID,
		).Scan(&u.ID, &u.Username, &u.Email, &u.FullName,
			&u.Status, &u.Provider, &u.ProviderID, &u.CreatedAt, &u.UpdatedAt)

		if err == sql.ErrNoRows {
			return writeError(c, http.StatusNotFound, "user not found")
		}
		if err != nil {
			return writeError(c, http.StatusInternalServerError, "failed to query user")
		}

		// Determine what changed (02-REQ-13.1: only bump updated_at on real changes).
		changed := false

		if req.FullName != nil {
			newFullName := *req.FullName
			// Per errata: full_name NOT NULL DEFAULT '' — store "" not SQL NULL.
			if newFullName != u.FullName {
				u.FullName = newFullName
				changed = true
			}
		}

		if req.Status != nil {
			if *req.Status != u.Status {
				u.Status = *req.Status
				changed = true
			}
		}

		// Execute UPDATE only if at least one field actually changed.
		if changed {
			now := time.Now().UTC().Format(time.RFC3339)
			u.UpdatedAt = now

			_, err = db.ExecContext(c.Request().Context(),
				`UPDATE users SET full_name = ?, status = ?, updated_at = ? WHERE id = ?`,
				u.FullName, u.Status, u.UpdatedAt, targetID,
			)
			if err != nil {
				return writeError(c, http.StatusInternalServerError, "failed to update user record")
			}
		}

		// Response includes provider_id but NOT team_memberships (02-REQ-7.1).
		// TeamMemberships left nil → omitted by omitempty tag.
		return c.JSON(http.StatusOK, u)
	}
}
