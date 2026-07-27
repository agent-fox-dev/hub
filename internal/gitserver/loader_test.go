package gitserver

import (
	"errors"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// TS-06-24: Loader.Load() extracts the workspace slug from the endpoint
// path, verifies clone_status=ready, opens the repository via PlainOpen,
// and returns (storer.Storer, nil).
// Requirement: 06-REQ-7.1
func TestLoader_HappyPath_ReadyWorkspace_ReturnsStorer(t *testing.T) {
	env := newGitTestEnv(t)

	// Seed org and workspace with matching org_id and clone_status='ready'.
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	// clone_status defaults to 'ready' from schema.

	// Create a real git repository at the expected filesystem path.
	env.initWorkspaceRepo(t, "myws")

	loader := NewWorkspaceLoader(env.db, env.workspaceRoot)
	ep := &transport.Endpoint{Path: "/git/myorg/myws.git"}

	s, err := loader.Load(ep)
	if err != nil {
		t.Errorf("Load() returned error: %v; want nil", err)
	}
	if s == nil {
		t.Error("Load() returned nil storer; want non-nil storer.Storer")
	}
}

// TestLoader_HappyPath_StorerIsUsable verifies that the storer returned
// by Load() can be used to access repository data (e.g., references).
// Requirement: 06-REQ-7.1
func TestLoader_HappyPath_StorerIsUsable(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	loader := NewWorkspaceLoader(env.db, env.workspaceRoot)
	ep := &transport.Endpoint{Path: "/git/myorg/myws.git"}

	s, err := loader.Load(ep)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if s == nil {
		t.Fatal("Load() returned nil storer")
	}

	// The storer should be able to iterate references (proving PlainOpen worked).
	iter, err := s.IterReferences()
	if err != nil {
		t.Errorf("storer.IterReferences() returned error: %v; want nil", err)
	}
	if iter == nil {
		t.Error("storer.IterReferences() returned nil iterator")
	}
}

// TS-06-25: Loader.Load() returns (nil, transport.ErrRepositoryNotFound)
// when the workspace is not found in the database.
// Requirement: 06-REQ-7.2
func TestLoader_WorkspaceNotFound_ReturnsErrRepositoryNotFound(t *testing.T) {
	env := newGitTestEnv(t)

	// No workspace seeded — 'ghost' does not exist.
	loader := NewWorkspaceLoader(env.db, env.workspaceRoot)
	ep := &transport.Endpoint{Path: "/git/myorg/ghost.git"}

	s, err := loader.Load(ep)
	if !errors.Is(err, transport.ErrRepositoryNotFound) {
		t.Errorf("Load() error = %v; want transport.ErrRepositoryNotFound", err)
	}
	if s != nil {
		t.Error("Load() returned non-nil storer; want nil")
	}
}

// TestLoader_WorkspaceNotReady_ReturnsErrRepositoryNotFound verifies
// that the Loader returns ErrRepositoryNotFound when the workspace
// exists but clone_status is not 'ready'.
// Requirement: 06-REQ-7.2
func TestLoader_WorkspaceNotReady_ReturnsErrRepositoryNotFound(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.setCloneStatus(t, "myws", "cloning")

	env.initWorkspaceRepo(t, "myws")

	loader := NewWorkspaceLoader(env.db, env.workspaceRoot)
	ep := &transport.Endpoint{Path: "/git/myorg/myws.git"}

	s, err := loader.Load(ep)
	if !errors.Is(err, transport.ErrRepositoryNotFound) {
		t.Errorf("Load() error = %v; want transport.ErrRepositoryNotFound", err)
	}
	if s != nil {
		t.Error("Load() returned non-nil storer for non-ready workspace; want nil")
	}
}

// TestLoader_OrgMismatch_ReturnsErrRepositoryNotFound verifies that the
// Loader returns ErrRepositoryNotFound when the org slug in the endpoint
// does not match the workspace's org_id.
// Requirement: 06-REQ-7.2
func TestLoader_OrgMismatch_ReturnsErrRepositoryNotFound(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrg(t, "org-2", "Other Org", "otherorg")
	env.seedOrgMember(t, "org-1", "user-1")
	// Workspace belongs to org-1 (slug 'myorg'), but endpoint uses 'otherorg'.
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	loader := NewWorkspaceLoader(env.db, env.workspaceRoot)
	ep := &transport.Endpoint{Path: "/git/otherorg/myws.git"}

	s, err := loader.Load(ep)
	if !errors.Is(err, transport.ErrRepositoryNotFound) {
		t.Errorf("Load() error = %v; want transport.ErrRepositoryNotFound", err)
	}
	if s != nil {
		t.Error("Load() returned non-nil storer for org mismatch; want nil")
	}
}

// TestLoader_InvalidEndpointPath_ReturnsErrRepositoryNotFound verifies
// that the Loader returns ErrRepositoryNotFound when the endpoint path
// does not contain a recognizable workspace slug segment.
// Requirement: 06-REQ-7.E2
func TestLoader_InvalidEndpointPath_ReturnsErrRepositoryNotFound(t *testing.T) {
	env := newGitTestEnv(t)

	loader := NewWorkspaceLoader(env.db, env.workspaceRoot)

	invalidPaths := []string{
		"",
		"/",
		"/git/",
		"/git/myorg/",
		"/some/random/path",
		"no-leading-slash",
	}

	for _, path := range invalidPaths {
		t.Run("path="+path, func(t *testing.T) {
			ep := &transport.Endpoint{Path: path}

			s, err := loader.Load(ep)
			if !errors.Is(err, transport.ErrRepositoryNotFound) {
				t.Errorf("Load(path=%q) error = %v; want transport.ErrRepositoryNotFound",
					path, err)
			}
			if s != nil {
				t.Errorf("Load(path=%q) returned non-nil storer; want nil", path)
			}
		})
	}
}

// TestLoader_PlainOpenFails_ReturnsWrappedError verifies that when PlainOpen
// returns an I/O error (not repository-not-found), the Loader wraps the
// error rather than returning transport.ErrRepositoryNotFound.
// Requirement: 06-REQ-7.E1
func TestLoader_PlainOpenFails_ReturnsWrappedError(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	// Deliberately do NOT create a git repo at the expected path.
	// The Loader should call PlainOpen and get an I/O error.

	loader := NewWorkspaceLoader(env.db, env.workspaceRoot)
	ep := &transport.Endpoint{Path: "/git/myorg/myws.git"}

	s, err := loader.Load(ep)
	if err == nil {
		t.Error("Load() returned nil error; want an error for missing repo dir")
	}
	if errors.Is(err, transport.ErrRepositoryNotFound) {
		t.Error("Load() returned transport.ErrRepositoryNotFound; want a wrapped I/O error " +
			"so the HTTP handler can distinguish 500 from 404")
	}
	if s != nil {
		t.Error("Load() returned non-nil storer for missing repo; want nil")
	}
}

// TestLoader_SlugExtraction verifies that the Loader correctly extracts
// the workspace slug from various endpoint path formats.
// Requirement: 06-REQ-7.1
func TestLoader_SlugExtraction(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "my-workspace", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "my-workspace")

	loader := NewWorkspaceLoader(env.db, env.workspaceRoot)

	// The standard path format used by git HTTP transport.
	ep := &transport.Endpoint{Path: "/git/myorg/my-workspace.git"}

	s, err := loader.Load(ep)
	if err != nil {
		t.Errorf("Load() returned error: %v; want nil", err)
	}
	if s == nil {
		t.Error("Load() returned nil storer; want non-nil")
	}
}
