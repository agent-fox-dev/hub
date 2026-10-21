package wsaccess

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/txsvc/apikit"
)

// RevokeAndUnbindSlug retires the tokens bound to slug inside tx (spec 34):
// it revokes those whose pats row is still live (revoked_at = apikit.NowUTC())
// and deletes every binding row of slug. It returns the ids it revoked,
// sorted, and an empty, non-nil slice when it revoked none. A slug without
// bindings issues no statement against pats, so it works on a database that
// has no pats table. Any error is returned, and the caller rolls tx back.
func RevokeAndUnbindSlug(tx *sql.Tx, slug string) ([]string, error) {
	bound, err := queryStrings(tx, `SELECT token_id FROM token_workspace_bindings WHERE slug = ?`, slug)
	if err != nil {
		return nil, fmt.Errorf("read the bindings of %q: %w", slug, err)
	}
	revoked := []string{}
	if len(bound) == 0 {
		return revoked, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(bound)), ",")
	args := make([]any, len(bound))
	for i, id := range bound {
		args[i] = id
	}
	live, err := queryStrings(tx, `SELECT token_id FROM pats WHERE revoked_at IS NULL AND token_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read the live tokens of %q: %w", slug, err)
	}
	if len(live) > 0 {
		liveArgs := make([]any, 0, len(live)+1)
		liveArgs = append(liveArgs, apikit.NowUTC())
		for _, id := range live {
			liveArgs = append(liveArgs, id)
		}
		if _, err := tx.Exec(`UPDATE pats SET revoked_at = ? WHERE revoked_at IS NULL AND token_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(live)), ",")+`)`, liveArgs...); err != nil {
			return nil, fmt.Errorf("revoke the tokens of %q: %w", slug, err)
		}
		revoked = append(revoked, live...)
		slices.Sort(revoked)
	}
	if _, err := tx.Exec(`DELETE FROM token_workspace_bindings WHERE slug = ?`, slug); err != nil {
		return nil, fmt.Errorf("delete the bindings of %q: %w", slug, err)
	}
	return revoked, nil
}

// queryStrings runs a query of one string column and returns its values,
// with the rows closed before it returns.
func queryStrings(tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
