package sandboxes

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// Scope is one place secrets are looked up: an owner type and an owner ID.
type Scope struct {
	Type    string
	OwnerID string
}

// SecretLister is all the lease service needs of the secrets store: the names
// of the secrets an owner holds. It has no way to read a value, so nothing
// built on it can leak one.
type SecretLister interface {
	ListSecrets(ownerType, ownerID string) ([]secrets.SecretEntry, error)
}

// MissingSecretsError reports required secrets that exist in no searched
// scope. Its message names the missing keys and the scope types searched, and
// no owner or org identifier.
type MissingSecretsError struct {
	// Missing are the missing key names, in the order the profile lists them.
	Missing []string
	// Searched are the scope types searched, in chain order.
	Searched []string
}

func (e *MissingSecretsError) Error() string {
	return fmt.Sprintf("required secrets not found: %s (searched scopes: %s)",
		strings.Join(e.Missing, ", "), strings.Join(e.Searched, ", "))
}

// ResolutionChain returns, in order, the scopes searched for a workspace's
// secrets: the workspace itself, the workspace's org when it has one and the
// workspace owner is a member of it, and the workspace owner's user scope. The
// user is always the workspace's owner, never the caller, so an admin token sees
// what the owner's agents would. The sandbox gateway resolves values through
// the same chain, so the check at issue and the lookup at creation agree.
func ResolutionChain(db *sql.DB, slug string) ([]Scope, error) {
	var owner string
	var org sql.NullString
	if err := db.QueryRow(`SELECT owner_id, org_id FROM workspaces WHERE slug = ?`, slug).Scan(&owner, &org); err != nil {
		return nil, fmt.Errorf("looking up the workspace for its secret scopes: %w", err)
	}

	chain := []Scope{{Type: "workspace", OwnerID: slug}}
	if org.Valid && org.String != "" {
		var members int
		if err := db.QueryRow(`SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?`, org.String, owner).Scan(&members); err != nil {
			return nil, fmt.Errorf("looking up the workspace owner's org membership: %w", err)
		}
		if members > 0 {
			chain = append(chain, Scope{Type: "org", OwnerID: org.String})
		}
	}
	return append(chain, Scope{Type: "user", OwnerID: owner}), nil
}

// CheckRequiredSecrets confirms that every required name exists in at least one
// scope of the workspace's resolution chain. Names match ignoring case, as the
// store's value lookup does. It lists key names only and never reads a value.
//
// It returns nil when all are present, a *MissingSecretsError when some are
// not, and any other error when a lookup failed. Optional secrets are not
// passed in: they are never checked.
func CheckRequiredSecrets(lister SecretLister, db *sql.DB, slug string, required []string) error {
	if len(required) == 0 {
		return nil
	}
	chain, err := ResolutionChain(db, slug)
	if err != nil {
		return err
	}

	have := map[string]bool{}
	searched := make([]string, 0, len(chain))
	for _, scope := range chain {
		searched = append(searched, scope.Type)
		entries, err := lister.ListSecrets(scope.Type, scope.OwnerID)
		if err != nil {
			return fmt.Errorf("listing the %s secrets: %w", scope.Type, err)
		}
		for _, e := range entries {
			have[strings.ToUpper(e.Key)] = true
		}
	}

	var missing []string
	for _, name := range required {
		if !have[strings.ToUpper(name)] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &MissingSecretsError{Missing: missing, Searched: searched}
	}
	return nil
}
