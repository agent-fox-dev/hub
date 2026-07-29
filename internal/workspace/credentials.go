package workspace

import (
	"context"
	"database/sql"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
)

// ValidateCredentialsFuncType is the signature for the function that validates
// git credentials by performing an ls-remote operation against the upstream
// repository.
//
// Parameters:
//   - ctx: context for cancellation and timeout
//   - gitURL: the upstream repository URL
//   - auth: the transport.AuthMethod to authenticate with (BasicAuth)
//
// Returns nil on success, or an error on authentication failure / timeout.
type ValidateCredentialsFuncType func(ctx context.Context, gitURL string, auth transport.AuthMethod) error

// validateCredentialsFn is the injectable credential validation function used
// by handleCreateWorkspace. Tests replace it to capture arguments or simulate
// errors. The production default (set during server init) uses NewRemote +
// memory.NewStorage() + Remote.ListContext with a 10-second deadline.
var validateCredentialsFn ValidateCredentialsFuncType

// CompensatingDeleteFuncType is the signature for the function that performs
// a compensating DELETE on a workspace row when CreateSecrets fails after a
// successful workspace INSERT (09-REQ-4.2). Tests replace it to simulate
// the double-failure scenario (09-REQ-4.3).
type CompensatingDeleteFuncType func(db *sql.DB, slug string) error

// compensatingDeleteFn is the injectable function used by handleCreateWorkspace
// to delete a workspace row when CreateSecrets fails. The production default
// performs a direct DELETE on the workspaces table (not deleteWorkspace, which
// cascade-deletes secrets/variables and would also fail if the secrets table
// is unavailable). Tests can replace it to simulate the double-failure path.
var compensatingDeleteFn CompensatingDeleteFuncType = defaultCompensatingDeleteFn

// defaultCompensatingDeleteFn performs a direct SQL DELETE on the workspaces
// table by slug. Uses a simple DELETE rather than deleteWorkspace() because
// the latter cascade-deletes from the secrets table, which may be the source
// of the CreateSecrets failure.
func defaultCompensatingDeleteFn(db *sql.DB, slug string) error {
	_, err := db.Exec("DELETE FROM workspaces WHERE slug = ?", slug)
	return err
}

// defaultValidateCredentialsFn is the production implementation of
// ValidateCredentialsFuncType. It creates an ephemeral remote with
// memory.NewStorage() and calls Remote.ListContext with a 10-second
// context deadline to perform an ls-remote credential check.
func defaultValidateCredentialsFn(ctx context.Context, gitURL string, auth transport.AuthMethod) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{gitURL},
	})

	_, err := remote.ListContext(ctx, &git.ListOptions{
		Auth: auth,
	})
	return err
}
