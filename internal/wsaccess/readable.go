package wsaccess

import (
	"database/sql"

	"github.com/txsvc/apikit"
)

// readableQuery selects every workspace, of any status, that the user (bound
// to all three parameters) owns or whose org the user is a member of, with
// the user's access to it. Ownership wins over membership, as in
// AuthorizeWorkspace.
const readableQuery = `SELECT slug, CASE WHEN owner_id = ? THEN 'owner' ELSE 'member' END
	FROM workspaces
	WHERE owner_id = ?
	   OR (org_id IS NOT NULL AND org_id != ''
	       AND org_id IN (SELECT org_id FROM org_members WHERE user_id = ?))`

// ReadableSlugs returns every workspace the caller in auth may read, mapped to
// the caller's access to it: the workspaces of any status the caller owns
// (AccessOwner) and those of the orgs the caller belongs to (AccessMember).
// A workspace is in the set exactly when AuthorizeWorkspace with NeedRead
// grants it, with the same Access.
//
// An admin token gets all true and a nil map, without a query. A nil auth,
// or a credential with no user id that is not an admin token, gets an empty
// non-nil map without a query. A PAT bound to a workspace gets at most that
// workspace, and only if it is in the caller's set. A query error is
// returned, with nothing granted.
func ReadableSlugs(db *sql.DB, auth *apikit.AuthInfo) (slugs map[string]Access, all bool, err error) {
	if auth != nil && isAdmin(auth) {
		return nil, true, nil
	}
	if auth == nil || auth.UserID == "" {
		return map[string]Access{}, false, nil
	}
	// The binding is resolved before any rows are opened, so that the call
	// works on a single-connection pool.
	boundSlug, bound, err := BoundSlug(db, auth)
	if err != nil {
		return nil, false, err
	}
	rows, err := db.Query(readableQuery, auth.UserID, auth.UserID, auth.UserID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	slugs = map[string]Access{}
	for rows.Next() {
		var slug string
		var access Access
		if err := rows.Scan(&slug, &access); err != nil {
			return nil, false, err
		}
		if bound && slug != boundSlug {
			continue
		}
		slugs[slug] = access
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return slugs, false, nil
}
