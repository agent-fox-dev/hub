package sandboxes

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// SecretReader is all the gateway needs of the secrets store to fill a box: the
// value of one secret of one owner. *secrets.Store is one. The values it returns
// reach a box's environment and nothing else.
type SecretReader interface {
	GetSecretValue(ownerType, ownerID, key string) (string, error)
}

// secretMissingError reports required secrets that exist in no scope of the
// workspace's resolution chain. Its text starts with secret_missing: and names
// the keys and the scope types searched, never an owner or an org id.
type secretMissingError struct {
	missing  []string
	searched []string
}

func (e *secretMissingError) Error() string {
	return fmt.Sprintf("secret_missing: %s (searched scopes: %s)", strings.Join(e.missing, ", "), strings.Join(e.searched, ", "))
}

// resolveSecrets reads the value of every required and every optional secret
// from the workspace's scopes, in the order of the chain the check at issue uses
// (see ResolutionChain): the first scope that has the name wins, and a scope
// that lacks it passes the lookup on. The values come back under the names as the
// profile spells them.
//
// A required name found in no scope is a *secretMissingError naming every such
// name; an optional name found in no scope is left out. Any other error from the
// store is returned as it is.
func resolveSecrets(reader SecretReader, db *sql.DB, slug string, required, optional []string) (map[string]string, error) {
	values := map[string]string{}
	if len(required)+len(optional) == 0 {
		return values, nil
	}
	if reader == nil {
		return nil, errors.New("no secrets store is configured")
	}
	chain, err := ResolutionChain(db, slug)
	if err != nil {
		return nil, err
	}

	lookup := func(name string) (value string, found bool, err error) {
		for _, scope := range chain {
			value, err = reader.GetSecretValue(scope.Type, scope.OwnerID, name)
			var notFound *secrets.NotFoundError
			switch {
			case err == nil:
				return value, true, nil
			case errors.As(err, &notFound):
				continue
			default:
				return "", false, err
			}
		}
		return "", false, nil
	}

	var missing []string
	for _, name := range required {
		value, found, err := lookup(name)
		switch {
		case err != nil:
			return nil, err
		case found:
			values[name] = value
		default:
			missing = append(missing, name)
		}
	}
	for _, name := range optional {
		value, found, err := lookup(name)
		if err != nil {
			return nil, err
		}
		if found {
			values[name] = value
		}
	}

	if len(missing) > 0 {
		searched := make([]string, 0, len(chain))
		for _, scope := range chain {
			searched = append(searched, scope.Type)
		}
		return nil, &secretMissingError{missing: missing, searched: searched}
	}
	return values, nil
}
