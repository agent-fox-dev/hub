package wsaccess

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/txsvc/apikit"
)

// The hub's permission scopes are resource:read and resource:write, one pair
// per resource (ADR 06). A PAT needs the scope of the verb it uses; a writer
// may read. Admin tokens and API keys are not scope-checked. CanRead and
// CanWrite are the one place that rule is written; the packages that keep a
// helper of their own for a message or an extra condition call HasScope with
// both scopes of the resource.

// CanRead reports whether the credential may read resource: a PAT needs
// resource:read or resource:write; every other credential type may.
func CanRead(auth *apikit.AuthInfo, resource string) bool {
	if !IsPAT(auth) {
		return true
	}
	return HasScope(auth, resource+":read", resource+":write")
}

// CanWrite reports whether the credential may change resource: a PAT needs
// resource:write; every other credential type may.
func CanWrite(auth *apikit.AuthInfo, resource string) bool {
	if !IsPAT(auth) {
		return true
	}
	return HasScope(auth, resource+":write")
}

// RenamedScopes maps every scope the hub registered before the read/write
// collapse to the scope that replaced it. A token that held one of the old
// scopes holds the new one after MigrateScopes.
var RenamedScopes = map[string]string{
	"workspaces:create": "workspaces:write",
	"workspaces:delete": "workspaces:write",
	"workspaces:sync":   "workspaces:write",
	"secrets:list":      "secrets:read",
	"secrets:manage":    "secrets:write",
	"secrets:delete":    "secrets:write",
	"vars:manage":       "vars:write",
	"vars:delete":       "vars:write",
}

// MigrateScopes rewrites the permissions of every stored PAT that names a
// renamed scope, replacing each old scope by its successor and dropping the
// duplicates that produces, in the order the token listed them. It returns the
// number of tokens rewritten. A second run finds nothing to rewrite.
func MigrateScopes(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT token_id, permissions FROM pats`)
	if err != nil {
		return 0, fmt.Errorf("read pats: %w", err)
	}
	type update struct{ id, perms string }
	var updates []update
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan pats: %w", err)
		}
		var perms []string
		if err := json.Unmarshal([]byte(raw), &perms); err != nil {
			continue // not this migration's problem; apikit reports it on use
		}
		renamed, changed := RenameScopes(perms)
		if !changed {
			continue
		}
		b, err := json.Marshal(renamed)
		if err != nil {
			rows.Close()
			return 0, err
		}
		updates = append(updates, update{id, string(b)})
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("read pats: %w", err)
	}
	for _, u := range updates {
		if _, err := db.Exec(`UPDATE pats SET permissions = ? WHERE token_id = ?`, u.perms, u.id); err != nil {
			return 0, fmt.Errorf("rewrite pat %s: %w", u.id, err)
		}
	}
	return len(updates), nil
}

// RenameScopes replaces every renamed scope in perms by its successor, keeps
// the first occurrence of each scope in the original order, and reports
// whether anything changed.
func RenameScopes(perms []string) ([]string, bool) {
	out := make([]string, 0, len(perms))
	changed := false
	for _, p := range perms {
		if to, ok := RenamedScopes[p]; ok {
			p = to
			changed = true
		}
		if slices.Contains(out, p) {
			changed = true
			continue
		}
		out = append(out, p)
	}
	return out, changed
}
