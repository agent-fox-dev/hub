package cli

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// apiPath joins path segments into an API path (without the /api/v1 prefix
// that DoRequest adds), percent-encoding every segment so user-supplied
// slugs, keys and ids cannot escape into a different route.
func apiPath(segments ...string) string {
	var b strings.Builder
	for _, seg := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(seg))
	}
	return b.String()
}

// escapePathKeepSlashes percent-encodes each slash-separated element of p
// while preserving the slashes, for wildcard routes that accept a path.
func escapePathKeepSlashes(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// readValueFromStdin reads a single value from the command's stdin. One
// trailing line break is removed so `echo value | afc ...` and here-strings
// behave as expected; other whitespace is preserved verbatim.
func readValueFromStdin(cmd *cobra.Command) (string, error) {
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", apikit.NewCLIError(2, fmt.Sprintf("failed to read value from stdin: %v", err))
	}
	v := string(data)
	v = strings.TrimSuffix(v, "\n")
	v = strings.TrimSuffix(v, "\r")
	return v, nil
}

// collectEntries parses the KEY=VALUE arguments of a create command. Every
// argument may itself be a comma-separated list. With fromStdin, exactly one
// bare KEY is expected and its value is read from stdin, which keeps secrets
// out of shell history and process listings.
func collectEntries(cmd *cobra.Command, args []string, fromStdin bool) ([]kvEntry, error) {
	if fromStdin {
		if len(args) != 1 || strings.Contains(args[0], "=") || strings.Contains(args[0], ",") {
			return nil, apikit.NewCLIError(2, "--from-stdin expects exactly one KEY argument")
		}
		if strings.TrimSpace(args[0]) == "" {
			return nil, apikit.NewCLIError(2, "empty key")
		}
		value, err := readValueFromStdin(cmd)
		if err != nil {
			return nil, err
		}
		return []kvEntry{{Key: args[0], Value: value}}, nil
	}
	var entries []kvEntry
	for _, arg := range args {
		parsed, err := parseKeyValueList(arg)
		if err != nil {
			return nil, err
		}
		entries = append(entries, parsed...)
	}
	return entries, nil
}

// singleEntry parses the argument of an update command: KEY=VALUE, or a
// bare KEY whose value is read from stdin when fromStdin is set.
func singleEntry(cmd *cobra.Command, arg string, fromStdin bool) (string, string, error) {
	if fromStdin {
		if strings.Contains(arg, "=") {
			return "", "", apikit.NewCLIError(2, "--from-stdin expects a bare KEY argument")
		}
		if strings.TrimSpace(arg) == "" {
			return "", "", apikit.NewCLIError(2, "empty key")
		}
		value, err := readValueFromStdin(cmd)
		if err != nil {
			return "", "", err
		}
		return arg, value, nil
	}
	return parseKeyValue(arg)
}

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
		targets = append(targets, scopeTarget{PathPrefix: apiPath("orgs", orgFlag)})
	}
	if workspaceFlag != "" {
		targets = append(targets, scopeTarget{PathPrefix: apiPath("workspaces", workspaceFlag)})
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
		return scopeTarget{PathPrefix: apiPath("orgs", orgFlag)}, nil
	}
	return scopeTarget{PathPrefix: apiPath("workspaces", workspaceFlag)}, nil
}
