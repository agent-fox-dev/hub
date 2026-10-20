package sandboxes

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/txsvc/apikit"
)

// The transitions of the sandbox gateway. Each is one UPDATE conditioned on the
// state the caller expects to find, and reports whether it changed a row, so
// that two callers racing for the same lease cannot both win. A lease in a
// terminal state is never touched. The endings (failed, destroyed) go through
// endLease, which clears the token hash in the same statement.

// MarkActive moves a leased lease to active and records the id of its box. The
// token stays valid: the holder uses it for every later call.
func (s *LeaseStore) MarkActive(id, sandboxID string) (bool, error) {
	var workspace string
	err := s.db.QueryRow(`UPDATE sandbox_leases SET state = 'active', sandbox_id = ?, updated_at = ?
		WHERE id = ? AND state = 'leased' RETURNING workspace`, sandboxID, apikit.FormatUTC(s.now()), id).Scan(&workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.metrics.activated(workspace)
	return true, nil
}

// FailLease moves a leased lease to failed and records message as its error. It
// has no end reason: the message says why.
func (s *LeaseStore) FailLease(id, message string) (bool, error) {
	return s.endLease(id, []string{leaseLeased}, leaseFailed, nil, &message)
}

// ExpireLease moves a leased lease to expired with the given end reason. It does
// not end an active lease: that one has a box, which ends as destroyed.
func (s *LeaseStore) ExpireLease(id, reason string) (bool, error) {
	return s.endLease(id, []string{leaseLeased}, leaseExpired, &reason, nil)
}

// DestroyLease moves a lease that is in state from, leased or active, to
// destroyed with the given end reason.
func (s *LeaseStore) DestroyLease(id, from, reason string) (bool, error) {
	if !slices.Contains([]string{leaseLeased, leaseActive}, from) {
		return false, fmt.Errorf("sandbox lease: %q is not a state a lease is destroyed from", from)
	}
	return s.endLease(id, []string{from}, leaseDestroyed, &reason, nil)
}
