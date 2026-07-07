// Package store provides typed CRUD operations for all database entities.
// It is the only package that executes SQL statements directly.
package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors returned by store operations.
var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("record not found")

	// ErrConstraintViolation is returned when a write violates a UNIQUE or
	// other database constraint.
	ErrConstraintViolation = errors.New("unique or database constraint violation")
)

// User represents a user record in the system.
type User struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	FullName   string `json:"full_name,omitempty"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// UserWithMemberships represents a user with their workspace memberships.
type UserWithMemberships struct {
	User
	Memberships []*WorkspaceMember `json:"memberships,omitempty"`
}

// Workspace represents a workspace record.
type Workspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by,omitempty"`
}

// WorkspaceMember represents a membership record.
type WorkspaceMember struct {
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
	GrantedBy   string `json:"granted_by,omitempty"`
}

// APIKey represents an API key record.
type APIKey struct {
	ID          string     `json:"id"`
	KeyID       string     `json:"key_id"`
	KeyHash     string     `json:"-"`
	UserID      string     `json:"user_id"`
	WorkspaceID string     `json:"workspace_id"`
	Role        string     `json:"role"`
	Label       string     `json:"label"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	CreatedAt   string     `json:"created_at"`
}

// AdminToken represents an admin token record.
type AdminToken struct {
	ID        string `json:"id"`
	TokenHash string `json:"-"`
	UserID    string `json:"user_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Store defines the data access interface for af-hub.
type Store interface {
	// Users
	CreateUser(u *User) (*User, error)
	GetUserByID(id string) (*User, error)
	GetUserByUsername(username string) (*User, error)
	GetUserByProviderID(provider, providerID string) (*User, error)
	UpdateUser(u *User) (*User, error)
	DeleteUser(id string) error
	ListUsers() ([]*User, error)
	CountUsers() (int, error)

	// Workspaces
	CreateWorkspace(w *Workspace) (*Workspace, error)
	GetWorkspaceByID(id string) (*Workspace, error)
	GetWorkspaceBySlug(slug string) (*Workspace, error)
	UpdateWorkspace(w *Workspace) (*Workspace, error)
	DeleteWorkspace(id string) error
	ListWorkspaces(includeArchived bool) ([]*Workspace, error)
	DeleteWorkspaceWithCascade(id string) error

	// Workspace Members
	CreateWorkspaceMember(m *WorkspaceMember) (*WorkspaceMember, error)
	GetWorkspaceMember(userID, workspaceID string) (*WorkspaceMember, error)
	ListWorkspaceMembers(workspaceID string) ([]*WorkspaceMember, error)
	DeleteWorkspaceMember(userID, workspaceID string) error
	UpsertWorkspaceMember(m *WorkspaceMember) (*WorkspaceMember, error)

	// API Keys
	CreateAPIKey(k *APIKey) (*APIKey, error)
	GetAPIKeyByID(id string) (*APIKey, error)
	GetAPIKeyByKeyID(keyID string) (*APIKey, error)
	RevokeAPIKey(id string) error
	DeleteAPIKey(id string) error
	ListAPIKeys() ([]*APIKey, error)
	ListAPIKeysByUserID(userID string) ([]*APIKey, error)
	CountAPIKeysByWorkspaceID(workspaceID string) (int, error)
	UpdateAPIKeyHash(keyID string, newHash string) error

	// Admin Tokens
	CreateAdminToken(t *AdminToken) (*AdminToken, error)
	GetAdminToken() (*AdminToken, error)
	GetAdminTokenByHash(hash string) (*AdminToken, error)
	UpdateAdminToken(t *AdminToken) (*AdminToken, error)
	DeleteAdminToken(id string) error
}

// sqliteStore is the concrete Store implementation backed by SQLite.
type sqliteStore struct {
	db *sql.DB
}

// DB returns the underlying database handle.
// Used primarily for testing (e.g. closing the DB to test error paths).
func (s *sqliteStore) DB() *sql.DB {
	return s.db
}

// NewStore creates a new Store backed by the given database.
func NewStore(db *sql.DB) *sqliteStore {
	return &sqliteStore{db: db}
}

// newID generates a new UUID string for use as an entity primary key.
func newID() string {
	return uuid.New().String()
}

// nowRFC3339 returns the current UTC time formatted as an RFC 3339 string.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// isConstraintError checks whether the given error is a SQLite UNIQUE or
// other constraint violation.
func isConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "constraint failed") ||
		strings.Contains(msg, "primary key constraint")
}
