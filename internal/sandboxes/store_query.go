package sandboxes

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

// maxListedLeases caps every list.
const maxListedLeases = 200

// leaseIDShape is the form of a lease id: sbx_ and 32 lowercase hex characters.
var leaseIDShape = regexp.MustCompile(`^sbx_[0-9a-f]{32}$`)

// ListLeases returns leases newest first, at most 200. A workspace of "" selects
// every workspace. Only live leases (leased or active) are returned unless all
// is true, which includes the terminal ones.
func (s *LeaseStore) ListLeases(workspace string, all bool) ([]*Lease, error) {
	query := `SELECT ` + leaseColumns + ` FROM sandbox_leases WHERE 1 = 1`
	var args []any
	if workspace != "" {
		query += ` AND workspace = ?`
		args = append(args, workspace)
	}
	if !all {
		query += ` AND state IN ('leased', 'active')`
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, maxListedLeases)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	leases := []*Lease{}
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		leases = append(leases, l)
	}
	return leases, rows.Err()
}

// GetWorkspaceLease returns the lease with this id in this workspace, in any
// state. A malformed id, an unknown id and the id of another workspace's lease
// are all ErrLeaseNotFound.
func (s *LeaseStore) GetWorkspaceLease(workspace, id string) (*Lease, error) {
	if !leaseIDShape.MatchString(id) {
		return nil, ErrLeaseNotFound
	}
	l, err := scanLease(s.db.QueryRow(`SELECT `+leaseColumns+` FROM sandbox_leases WHERE id = ? AND workspace = ?`, id, workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLeaseNotFound
	}
	return l, err
}
