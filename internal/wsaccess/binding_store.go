package wsaccess

import (
	"context"
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

// BoundToken is one token bound to a workspace, joined with its pats row.
type BoundToken struct {
	TokenID     string
	Name        string
	Permissions string // the pats row's JSON array, as stored
	ExpiresAt   sql.NullString
	RevokedAt   sql.NullString
	CreatedAt   string
}

// ListBoundTokens returns the tokens bound to slug, joined with their pats
// rows and ordered by the token's created_at and then its id. Revoked and
// expired tokens are included; a binding whose pats row is gone is left out.
// It never reads a secret.
func ListBoundTokens(db *sql.DB, slug string) ([]BoundToken, error) {
	rows, err := db.Query(`SELECT p.token_id, p.name, p.permissions, p.expires_at, p.revoked_at, p.created_at
		FROM token_workspace_bindings b JOIN pats p ON p.token_id = b.token_id
		WHERE b.slug = ?
		ORDER BY p.created_at, p.token_id`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BoundToken{}
	for rows.Next() {
		var bt BoundToken
		if err := rows.Scan(&bt.TokenID, &bt.Name, &bt.Permissions, &bt.ExpiresAt, &bt.RevokedAt, &bt.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, bt)
	}
	return out, rows.Err()
}

// InsertBinding writes the binding row (tokenID, slug, createdAt), but only
// while the workspaces row of slug exists: it reports written false and a nil
// error when the workspace is gone. A database error, a primary-key conflict
// included, is returned. SQLite serialises writes, so the insert is ordered
// against a concurrent deletion of the workspace.
func InsertBinding(db *sql.DB, tokenID, slug, createdAt string) (bool, error) {
	res, err := db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at)
		SELECT ?, slug, ? FROM workspaces WHERE slug = ?`, tokenID, createdAt, slug)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// RevokeToken revokes tokenID the way apikit's own revocation does: it sets
// revoked_at = apikit.NowUTC() on its pats row when the row is still live.
func RevokeToken(db *sql.DB, tokenID string) error {
	_, err := db.Exec(`UPDATE pats SET revoked_at = ? WHERE token_id = ? AND revoked_at IS NULL`, apikit.NowUTC(), tokenID)
	return err
}

// SweepBindings is one pass of the binding sweep (spec 34), in one
// transaction: for every slug with bindings but no workspaces row it revokes
// the live tokens and deletes the bindings (as RevokeAndUnbindSlug does),
// then it deletes the bindings whose token has no pats row. Bindings of
// revoked or expired tokens whose workspace exists are kept. Any error rolls
// the whole pass back; a cancelled ctx ends it.
func SweepBindings(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	orphans, err := queryStrings(tx, `SELECT DISTINCT slug FROM token_workspace_bindings
		WHERE slug NOT IN (SELECT slug FROM workspaces)`)
	if err != nil {
		return fmt.Errorf("read the bindings of missing workspaces: %w", err)
	}
	for _, slug := range orphans {
		if _, err := RevokeAndUnbindSlug(tx, slug); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM token_workspace_bindings
		WHERE token_id NOT IN (SELECT token_id FROM pats)`); err != nil {
		return fmt.Errorf("delete the bindings of missing tokens: %w", err)
	}
	return tx.Commit()
}
