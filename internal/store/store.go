// Package store provides typed CRUD operations for all database entities.
// It is the only package that executes SQL statements directly.
package store

import (
	"database/sql"
	"errors"
)

// Sentinel errors returned by store operations.
var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("record not found")

	// ErrConstraintViolation is returned when a write violates a UNIQUE or
	// other database constraint.
	ErrConstraintViolation = errors.New("constraint violation")
)

// User represents a row in the users table.
type User struct {
	ID         string
	Username   string
	Email      string
	FullName   string
	Provider   string
	ProviderID string
	Status     string
	CreatedAt  string
	UpdatedAt  string
}

// Workspace represents a row in the workspaces table.
type Workspace struct {
	ID        string
	Name      string
	Slug      string
	URL       string
	Status    string
	CreatedAt string
	CreatedBy string
}

// WorkspaceMember represents a row in the workspace_members table.
type WorkspaceMember struct {
	UserID      string
	WorkspaceID string
	Role        string
	CreatedAt   string
	GrantedBy   string
}

// APIKey represents a row in the api_keys table.
type APIKey struct {
	ID          string
	KeyID       string
	KeyHash     string
	UserID      string
	WorkspaceID string
	Label       string
	ExpiresAt   string
	RevokedAt   string
	CreatedAt   string
}

// AdminToken represents a row in the admin_tokens table.
type AdminToken struct {
	ID        string
	TokenHash string
	CreatedAt string
}

// Store provides CRUD operations for all database entities.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store backed by the given database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB for health checks and other
// infrastructure needs. Callers must NOT execute SQL directly.
func (s *Store) DB() *sql.DB {
	return s.db
}

// --- Users ---

// CreateUser inserts a new user record. The ID and timestamps are
// generated automatically.
func (s *Store) CreateUser(u *User) (*User, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetUserByID retrieves a user by primary key.
func (s *Store) GetUserByID(id string) (*User, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetUserByUsername retrieves a user by username.
func (s *Store) GetUserByUsername(username string) (*User, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetUserByProviderID retrieves a user by (provider, provider_id).
func (s *Store) GetUserByProviderID(provider, providerID string) (*User, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// UpdateUser persists changes to an existing user record.
func (s *Store) UpdateUser(u *User) (*User, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// DeleteUser removes a user by ID.
func (s *Store) DeleteUser(id string) error {
	// Stub — implementation in a later task group.
	return nil
}

// CountUsers returns the number of rows in the users table.
func (s *Store) CountUsers() (int, error) {
	// Stub — implementation in a later task group.
	return 0, nil
}

// --- Workspaces ---

// CreateWorkspace inserts a new workspace record.
func (s *Store) CreateWorkspace(w *Workspace) (*Workspace, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetWorkspaceByID retrieves a workspace by primary key.
func (s *Store) GetWorkspaceByID(id string) (*Workspace, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetWorkspaceBySlug retrieves a workspace by slug.
func (s *Store) GetWorkspaceBySlug(slug string) (*Workspace, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// UpdateWorkspace persists changes to an existing workspace.
func (s *Store) UpdateWorkspace(w *Workspace) (*Workspace, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// DeleteWorkspace removes a workspace by ID.
func (s *Store) DeleteWorkspace(id string) error {
	// Stub — implementation in a later task group.
	return nil
}

// --- Workspace Members ---

// CreateWorkspaceMember inserts a new membership record.
func (s *Store) CreateWorkspaceMember(m *WorkspaceMember) (*WorkspaceMember, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetWorkspaceMember retrieves a membership by (user_id, workspace_id).
func (s *Store) GetWorkspaceMember(userID, workspaceID string) (*WorkspaceMember, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// ListWorkspaceMembers lists all members of a workspace.
func (s *Store) ListWorkspaceMembers(workspaceID string) ([]*WorkspaceMember, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// DeleteWorkspaceMember removes a membership.
func (s *Store) DeleteWorkspaceMember(userID, workspaceID string) error {
	// Stub — implementation in a later task group.
	return nil
}

// --- API Keys ---

// CreateAPIKey inserts a new API key record.
func (s *Store) CreateAPIKey(k *APIKey) (*APIKey, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetAPIKeyByID retrieves an API key by primary key.
func (s *Store) GetAPIKeyByID(id string) (*APIKey, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetAPIKeyByKeyID retrieves an API key by key_id.
func (s *Store) GetAPIKeyByKeyID(keyID string) (*APIKey, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// RevokeAPIKey sets the revoked_at timestamp on an API key.
func (s *Store) RevokeAPIKey(id string) error {
	// Stub — implementation in a later task group.
	return nil
}

// DeleteAPIKey removes an API key by ID.
func (s *Store) DeleteAPIKey(id string) error {
	// Stub — implementation in a later task group.
	return nil
}

// --- Admin Tokens ---

// CreateAdminToken inserts a new admin token record.
func (s *Store) CreateAdminToken(t *AdminToken) (*AdminToken, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GetAdminToken retrieves the current admin token record.
func (s *Store) GetAdminToken() (*AdminToken, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// UpdateAdminToken replaces the admin token hash.
func (s *Store) UpdateAdminToken(t *AdminToken) (*AdminToken, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// DeleteAdminToken removes an admin token by ID.
func (s *Store) DeleteAdminToken(id string) error {
	// Stub — implementation in a later task group.
	return nil
}
