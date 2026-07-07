// Package store provides typed CRUD operations for all database entities.
// It is the only package that executes SQL statements directly.
package store

import (
	"database/sql"
	"errors"
	"time"
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

// --- Stub implementations (to be completed in task group 6) ---

func (s *sqliteStore) CreateUser(u *User) (*User, error) {
	return nil, errors.New("store: create user: not implemented")
}

func (s *sqliteStore) GetUserByID(id string) (*User, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) GetUserByUsername(username string) (*User, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) GetUserByProviderID(provider, providerID string) (*User, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) UpdateUser(u *User) (*User, error) {
	return nil, errors.New("store: update user: not implemented")
}

func (s *sqliteStore) DeleteUser(id string) error {
	return errors.New("store: delete user: not implemented")
}

func (s *sqliteStore) ListUsers() ([]*User, error) {
	return nil, errors.New("store: list users: not implemented")
}

func (s *sqliteStore) CountUsers() (int, error) {
	return 0, errors.New("store: count users: not implemented")
}

func (s *sqliteStore) CreateWorkspace(w *Workspace) (*Workspace, error) {
	return nil, errors.New("store: create workspace: not implemented")
}

func (s *sqliteStore) GetWorkspaceByID(id string) (*Workspace, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) GetWorkspaceBySlug(slug string) (*Workspace, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) UpdateWorkspace(w *Workspace) (*Workspace, error) {
	return nil, errors.New("store: update workspace: not implemented")
}

func (s *sqliteStore) DeleteWorkspace(id string) error {
	return errors.New("store: delete workspace: not implemented")
}

func (s *sqliteStore) ListWorkspaces(includeArchived bool) ([]*Workspace, error) {
	return nil, errors.New("store: list workspaces: not implemented")
}

func (s *sqliteStore) DeleteWorkspaceWithCascade(id string) error {
	return errors.New("store: delete workspace cascade: not implemented")
}

func (s *sqliteStore) CreateWorkspaceMember(m *WorkspaceMember) (*WorkspaceMember, error) {
	return nil, errors.New("store: create workspace member: not implemented")
}

func (s *sqliteStore) GetWorkspaceMember(userID, workspaceID string) (*WorkspaceMember, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) ListWorkspaceMembers(workspaceID string) ([]*WorkspaceMember, error) {
	return nil, errors.New("store: list workspace members: not implemented")
}

func (s *sqliteStore) DeleteWorkspaceMember(userID, workspaceID string) error {
	return errors.New("store: delete workspace member: not implemented")
}

func (s *sqliteStore) UpsertWorkspaceMember(m *WorkspaceMember) (*WorkspaceMember, error) {
	return nil, errors.New("store: upsert workspace member: not implemented")
}

func (s *sqliteStore) CreateAPIKey(k *APIKey) (*APIKey, error) {
	return nil, errors.New("store: create api key: not implemented")
}

func (s *sqliteStore) GetAPIKeyByID(id string) (*APIKey, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) GetAPIKeyByKeyID(keyID string) (*APIKey, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) RevokeAPIKey(id string) error {
	return errors.New("store: revoke api key: not implemented")
}

func (s *sqliteStore) DeleteAPIKey(id string) error {
	return errors.New("store: delete api key: not implemented")
}

func (s *sqliteStore) ListAPIKeys() ([]*APIKey, error) {
	return nil, errors.New("store: list api keys: not implemented")
}

func (s *sqliteStore) ListAPIKeysByUserID(userID string) ([]*APIKey, error) {
	return nil, errors.New("store: list api keys by user: not implemented")
}

func (s *sqliteStore) CountAPIKeysByWorkspaceID(workspaceID string) (int, error) {
	return 0, errors.New("store: count api keys by workspace: not implemented")
}

func (s *sqliteStore) UpdateAPIKeyHash(keyID string, newHash string) error {
	return errors.New("store: update api key hash: not implemented")
}

func (s *sqliteStore) CreateAdminToken(t *AdminToken) (*AdminToken, error) {
	return nil, errors.New("store: create admin token: not implemented")
}

func (s *sqliteStore) GetAdminToken() (*AdminToken, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) GetAdminTokenByHash(hash string) (*AdminToken, error) {
	return nil, ErrNotFound
}

func (s *sqliteStore) UpdateAdminToken(t *AdminToken) (*AdminToken, error) {
	return nil, errors.New("store: update admin token: not implemented")
}

func (s *sqliteStore) DeleteAdminToken(id string) error {
	return errors.New("store: delete admin token: not implemented")
}
