package cli

import (
	"fmt"
	"strings"

	"github.com/txsvc/apikit"
)

// kvEntry represents a single key=value pair parsed from CLI arguments.
type kvEntry struct {
	Key   string
	Value string
}

// parseKeyValue splits arg on the first '=' character.
// The key is everything before the first '=' and the value is everything
// after (the value may contain additional '=' characters).
// Returns an error if no '=' is present.
func parseKeyValue(arg string) (string, string, error) {
	parts := strings.SplitN(arg, "=", 2)
	if len(parts) < 2 {
		return "", "", apikit.NewCLIError(2, fmt.Sprintf("invalid argument: missing '=' in '%s'", arg))
	}
	return parts[0], parts[1], nil
}

// parseKeyValueList splits a comma-separated argument into individual
// key=value entries. Each token is parsed via parseKeyValue. If any token
// produces an empty or whitespace-only key, an error is returned identifying
// the zero-based position of the empty entry.
func parseKeyValueList(arg string) ([]kvEntry, error) {
	tokens := strings.Split(arg, ",")
	entries := make([]kvEntry, 0, len(tokens))

	for i, token := range tokens {
		if strings.TrimSpace(token) == "" {
			return nil, apikit.NewCLIError(2, fmt.Sprintf("empty key in argument at position %d", i))
		}

		key, value, err := parseKeyValue(token)
		if err != nil {
			return nil, err
		}

		if strings.TrimSpace(key) == "" {
			return nil, apikit.NewCLIError(2, fmt.Sprintf("empty key in argument at position %d", i))
		}

		entries = append(entries, kvEntry{Key: key, Value: value})
	}

	return entries, nil
}

// scopeTarget holds the API path prefix for a single ownership scope.
type scopeTarget struct {
	// PathPrefix is the API path prefix without /api/v1 (e.g., "/user",
	// "/orgs/myorg", "/workspaces/myws"). DoRequest prepends /api/v1.
	PathPrefix string
}

// resolveScope returns the ordered list of scope targets implied by the
// ownership flags. If all three flags are at zero values, it defaults to
// user scope. Otherwise each non-zero flag is included in fixed order:
// user -> org -> workspace.
//
// This function is used by create commands that support multi-scope writes.
func resolveScope(userFlag bool, orgFlag, workspaceFlag string) []scopeTarget {
	// If no flags are provided, default to user scope.
	if !userFlag && orgFlag == "" && workspaceFlag == "" {
		return []scopeTarget{{PathPrefix: "/user"}}
	}

	var targets []scopeTarget
	if userFlag {
		targets = append(targets, scopeTarget{PathPrefix: "/user"})
	}
	if orgFlag != "" {
		targets = append(targets, scopeTarget{PathPrefix: "/orgs/" + orgFlag})
	}
	if workspaceFlag != "" {
		targets = append(targets, scopeTarget{PathPrefix: "/workspaces/" + workspaceFlag})
	}

	return targets
}

// singleScope returns exactly one scope target for list, update, and delete
// commands. If multiple ownership flags are set, it returns an error. If no
// flags are provided, it defaults to user scope.
func singleScope(userFlag bool, orgFlag, workspaceFlag string) (scopeTarget, error) {
	count := 0
	if userFlag {
		count++
	}
	if orgFlag != "" {
		count++
	}
	if workspaceFlag != "" {
		count++
	}

	if count > 1 {
		return scopeTarget{}, apikit.NewCLIError(2, "only one of --user, --org, --workspace may be specified")
	}

	// If no flags, default to user scope.
	if count == 0 {
		return scopeTarget{PathPrefix: "/user"}, nil
	}

	if userFlag {
		return scopeTarget{PathPrefix: "/user"}, nil
	}
	if orgFlag != "" {
		return scopeTarget{PathPrefix: "/orgs/" + orgFlag}, nil
	}
	return scopeTarget{PathPrefix: "/workspaces/" + workspaceFlag}, nil
}
